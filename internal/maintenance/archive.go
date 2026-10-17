package maintenance

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/settings"
	"tillbox/internal/store"
)

type record struct {
	Version int    `json:"version"`
	Table   string `json:"table"`
	ID      int64  `json:"id"`
	Data    any    `json:"data"`
}

func (s *Service) batch(ctx context.Context, retention settings.Retention, now time.Time) (int64, error) {
	var count int64
	err := s.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		cut := func(days int) int64 { return now.Add(-time.Duration(days) * 24 * time.Hour).UnixMilli() }
		var txns []store.Transaction
		err := tx.NewSelect().Model(&txns).Where("t.occurred_at < ?", cut(retention.RawDays)).Where("t.raw <> ''").
			Where("t.state IN (?)", bun.List([]string{store.TxnMatched, store.TxnIgnored})).
			Where("NOT EXISTS (SELECT 1 FROM anomalies a WHERE (a.transaction_id = t.id OR (t.order_id <> 0 AND a.order_id = t.order_id)) AND a.status = ?)", store.AnomalyOpen).
			Where("t.order_id = 0 OR EXISTS (SELECT 1 FROM orders o WHERE o.id = t.order_id AND o.status = ?)", store.OrderPaid).
			Order("t.id").Limit(500).Scan(ctx)
		if err != nil {
			return err
		}
		var attempts []store.NotifyAttempt
		err = tx.NewSelect().Model(&attempts).Where("created_at < ?", cut(retention.AttemptDays)).
			Where("order_id IN (SELECT order_id FROM notifications WHERE status = ?)", store.NotifySuccess).
			Where("order_id IN (SELECT id FROM orders WHERE status = ?)", store.OrderPaid).
			Where("order_id NOT IN (SELECT order_id FROM anomalies WHERE status = ?)", store.AnomalyOpen).
			Where("id NOT IN (SELECT MAX(id) FROM notify_attempts GROUP BY order_id)").Order("id").Limit(500).Scan(ctx)
		if err != nil {
			return err
		}
		var audits []store.AuditLog
		if err := tx.NewSelect().Model(&audits).Where("created_at < ?", cut(retention.AuditDays)).Order("id").Limit(500).Scan(ctx); err != nil {
			return err
		}
		count = int64(len(txns) + len(attempts) + len(audits))
		if count == 0 {
			return nil
		}
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		enc := json.NewEncoder(gz)
		for _, t := range txns {
			if err := enc.Encode(record{1, "transactions.raw", t.ID, t.Raw}); err != nil {
				return err
			}
		}
		for _, a := range attempts {
			if err := enc.Encode(record{1, "notify_attempts", a.ID, a}); err != nil {
				return err
			}
		}
		for _, a := range audits {
			if err := enc.Encode(record{1, "audit_logs", a.ID, a}); err != nil {
				return err
			}
		}
		if err := gz.Close(); err != nil {
			return err
		}
		sealed, err := s.Settings.Box().Seal(buf.String())
		if err != nil {
			return err
		}
		if len(sealed) > 64<<20 {
			return fmt.Errorf("archive batch exceeds size limit")
		}
		if err := os.MkdirAll(s.Dir, 0700); err != nil {
			return err
		}
		name, err := archiveName(now)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(filepath.Join(s.Dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := file.WriteString(sealed); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		// Persist the directory entry before committing any pruning. A rollback
		// may leave an extra encrypted file, which is safe to retain for recovery.
		dir, err := os.Open(s.Dir)
		if err != nil {
			return err
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(sealed))
		batch := &store.ArchiveBatch{Filename: name, SHA256: hex.EncodeToString(sum[:]), Records: count, CreatedAt: now.UnixMilli()}
		if _, err := tx.NewInsert().Model(batch).Exec(ctx); err != nil {
			return err
		}
		for _, t := range txns {
			res, err := tx.NewUpdate().Model((*store.Transaction)(nil)).Set("raw = ''").Where("id = ? AND raw = ?", t.ID, t.Raw).Exec(ctx)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return fmt.Errorf("transaction changed during archive")
			}
		}
		for _, a := range attempts {
			if _, err := tx.NewDelete().Model(&a).WherePK().Exec(ctx); err != nil {
				return err
			}
		}
		for _, a := range audits {
			if _, err := tx.NewDelete().Model(&a).WherePK().Exec(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
