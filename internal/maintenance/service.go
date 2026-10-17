// Package maintenance archives operational evidence before pruning it from the
// live database. Orders, amounts, ledger fingerprints and links are never removed.
package maintenance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/settings"
	"tillbox/internal/store"
)

type State struct {
	LastRunAt int64  `json:"last_run_at"`
	LastOKAt  int64  `json:"last_ok_at"`
	LastError string `json:"last_error"`
	Archived  int64  `json:"archived"`
}

type Service struct {
	DB       *bun.DB
	Settings *settings.Service
	Dir      string
	mu       sync.Mutex
	Now      func() time.Time
}

func New(db *bun.DB, st *settings.Service, dataDir string) *Service {
	return &Service{DB: db, Settings: st, Dir: filepath.Join(dataDir, "archives"), Now: time.Now}
}

func (s *Service) Status(ctx context.Context) (State, error) {
	var v State
	_, err := s.Settings.Raw(ctx, "maintenance_state", &v)
	return v, err
}

func (s *Service) Run(ctx context.Context, force bool) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.Status(ctx)
	if err != nil {
		return v, err
	}
	r, err := s.Settings.Retention(ctx)
	if err != nil {
		return v, err
	}
	now := s.Now()
	if !r.Enabled || (!force && v.LastRunAt > now.Add(-24*time.Hour).UnixMilli()) {
		return v, nil
	}
	v.LastRunAt, v.LastError, v.Archived = now.UnixMilli(), "", 0
	for range 10 { // bound each daily run to 15,000 records
		n, runErr := s.batch(ctx, r, now)
		if runErr != nil {
			v.LastError = "归档失败，数据库原记录保留；请检查数据目录权限与空间"
			err = runErr
			break
		}
		v.Archived += n
		if n == 0 {
			break
		}
	}
	if err == nil {
		v.LastOKAt = now.UnixMilli()
	}
	if saveErr := s.Settings.SaveRaw(ctx, "maintenance_state", v); saveErr != nil {
		return v, saveErr
	}
	return v, err
}

func (s *Service) Read(ctx context.Context, id int64) ([]byte, *store.ArchiveBatch, error) {
	var batch store.ArchiveBatch
	if err := s.DB.NewSelect().Model(&batch).Where("id = ?", id).Scan(ctx); err != nil {
		return nil, nil, err
	}
	if filepath.Base(batch.Filename) != batch.Filename || !strings.HasSuffix(batch.Filename, ".jsonl.gz.enc") {
		return nil, nil, fmt.Errorf("invalid archive path")
	}
	f, err := os.Open(filepath.Join(s.Dir, batch.Filename))
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if stat.Size() > 64<<20 {
		return nil, nil, fmt.Errorf("archive exceeds limit")
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		return nil, nil, err
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != batch.SHA256 {
		return nil, nil, fmt.Errorf("archive checksum mismatch")
	}
	plain, err := s.Settings.Box().Open(string(raw))
	return []byte(plain), &batch, err
}

func archiveName(now time.Time) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:]) + ".jsonl.gz.enc", nil
}
