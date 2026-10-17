package maintenance_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/order"
	"tillbox/internal/store"
)

func fixture(t *testing.T) (*apptest.Env, *store.Order) {
	t.Helper()
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	o, _, err := e.App.Orders.Create(e.Ctx, order.CreateInput{MerchantID: m.ID, OutTradeNo: "archive", Version: 1, Channel: m.Channel, PayType: "alipay", Name: "archive", Money: "1.00", Amount: 100, NotifyURL: e.Hook.URL})
	if err != nil {
		t.Fatal(err)
	}
	e.Pay(o.TradeNo)
	e.Deliver()
	if _, err := e.App.Notifier.Resend(e.Ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	o = e.Order(o.TradeNo)
	old := time.Now().Add(-400 * 24 * time.Hour).UnixMilli()
	if _, err := e.DB().NewUpdate().Model((*store.Transaction)(nil)).Set("occurred_at = ?", old).Set("raw = ?", `{"evidence":"archived"}`).Where("id = ?", o.TxnID).Exec(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB().NewUpdate().Model((*store.NotifyAttempt)(nil)).Set("created_at = ?", old).Where("order_id = ?", o.ID).Exec(e.Ctx); err != nil {
		t.Fatal(err)
	}
	audit := &store.AuditLog{Actor: "admin", Action: "old.action", Detail: "durable-audit-evidence", CreatedAt: old}
	if _, err := e.DB().NewInsert().Model(audit).Exec(e.Ctx); err != nil {
		t.Fatal(err)
	}
	return e, o
}

func TestArchiveRoundtripAndIdempotency(t *testing.T) {
	e, o := fixture(t)
	s := e.App.Maintenance
	v, err := s.Run(e.Ctx, true)
	if err != nil || v.Archived != 3 {
		t.Fatalf("archive: %+v %v", v, err)
	}
	var batches []store.ArchiveBatch
	if err := e.DB().NewSelect().Model(&batches).Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 {
		t.Fatal("batch missing")
	}
	content, batch, err := s.Read(e.Ctx, batches[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(gz)
	gz.Close()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(plain)), "\n")
	if len(lines) != 3 || !strings.Contains(string(plain), "archived") || !strings.Contains(string(plain), "durable-audit-evidence") {
		t.Fatalf("lost evidence: %s", plain)
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatal("invalid jsonl")
		}
	}
	var txn store.Transaction
	if err := e.DB().NewSelect().Model(&txn).Where("id = ?", o.TxnID).Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if txn.Raw != "" || txn.OrderID != o.ID || e.Order(o.TradeNo).Status != store.OrderPaid {
		t.Fatal("core financial records damaged")
	}
	n, err := e.DB().NewSelect().Model((*store.NotifyAttempt)(nil)).Where("order_id = ?", o.ID).Count(e.Ctx)
	if err != nil || n != 1 {
		t.Fatal("last attempt not retained")
	}
	v, err = s.Run(e.Ctx, true)
	if err != nil || v.Archived != 0 {
		t.Fatal("archive not idempotent")
	}
	file := filepath.Join(s.Dir, batch.Filename)
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe permissions")
	}
	if err := os.WriteFile(file, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Read(e.Ctx, batch.ID); err == nil {
		t.Fatal("corrupted archive accepted")
	}
}

func TestArchiveFailureAndOpenAnomaly(t *testing.T) {
	e, o := fixture(t)
	s := e.App.Maintenance
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Dir = blocked
	if _, err := s.Run(e.Ctx, true); err == nil {
		t.Fatal("expected archive failure")
	}
	var txn store.Transaction
	if err := e.DB().NewSelect().Model(&txn).Where("id = ?", o.TxnID).Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if txn.Raw == "" {
		t.Fatal("data pruned before durable archive")
	}
	s.Dir = filepath.Join(t.TempDir(), "archives")
	anomaly := &store.Anomaly{Kind: store.AnomalyConflict, TransactionID: o.TxnID, OrderID: o.ID, Status: store.AnomalyOpen, CreatedAt: store.Now()}
	if _, err := e.DB().NewInsert().Model(anomaly).Exec(e.Ctx); err != nil {
		t.Fatal(err)
	}
	v, err := s.Run(e.Ctx, true)
	if err != nil || v.Archived != 1 {
		t.Fatalf("open anomaly evidence pruned: %+v %v", v, err)
	}
	if err := e.DB().NewSelect().Model(&txn).Where("id = ?", o.TxnID).Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if txn.Raw == "" {
		t.Fatal("open anomaly raw lost")
	}
}
