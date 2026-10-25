package backup_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/backup"
	"tillbox/internal/settings"
)

func TestBackup(t *testing.T) {
	e := apptest.New(t)
	b := e.App.Backup
	if !b.Supported() {
		if _, err := b.Run(e.Ctx, true); !errors.Is(err, backup.ErrUnsupported) {
			t.Fatalf("PostgreSQL 不支持内置备份，应返回 ErrUnsupported：%v", err)
		}
		return
	}
	e.NewMerchant(nil)
	now := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	b.Now = func() time.Time { return now }

	// 默认关着，不自动备份。
	if st, err := b.Run(e.Ctx, false); err != nil || st.LastRunAt != 0 {
		t.Fatalf("默认不该自动备份：%+v %v", st, err)
	}
	if err := e.App.Settings.SaveBackup(e.Ctx, settings.Backup{Enabled: true, Keep: 7}); err != nil {
		t.Fatal(err)
	}
	st, err := b.Run(e.Ctx, false)
	if err != nil || st.LastError != "" || st.LastFile != "tillbox-20261025T030000Z.db" {
		t.Fatalf("打开后第一次应马上备份：%+v %v", st, err)
	}
	path := filepath.Join(b.Dir, st.LastFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("备份文件权限应为 600，实际 %v", info.Mode().Perm())
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM merchants").Scan(&n); err != nil || n != 1 {
		t.Errorf("备份里应有刚建的商户：%d %v", n, err)
	}
	db.Close()

	// 当天再跑不重复备份；过了一天再备份一份。
	now = now.Add(time.Hour)
	if st, _ = b.Run(e.Ctx, false); st.LastFile != "tillbox-20261025T030000Z.db" {
		t.Errorf("一天内不应重复备份：%+v", st)
	}
	if err := e.App.Settings.SaveBackup(e.Ctx, settings.Backup{Enabled: true, Keep: 2}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		now = now.Add(24 * time.Hour)
		if _, err := b.Run(e.Ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	files, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name != "tillbox-20261028T040000Z.db" || files[1].Name != "tillbox-20261027T040000Z.db" {
		t.Fatalf("应只留最近 2 份，新的在前：%+v", files)
	}
	if files[0].Size == 0 || files[0].CreatedAt != time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("备份文件信息不对：%+v", files[0])
	}

	// 关掉后不自动备份，立即备份不受影响。
	if err := e.App.Settings.SaveBackup(e.Ctx, settings.Backup{Enabled: false, Keep: 2}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	if st, _ = b.Run(e.Ctx, false); st.LastFile != "tillbox-20261028T040000Z.db" {
		t.Errorf("关掉后不应自动备份：%+v", st)
	}
	if st, err = b.Run(e.Ctx, true); err != nil || st.LastFile != "tillbox-20261030T040000Z.db" {
		t.Errorf("立即备份不看开关：%+v %v", st, err)
	}
}

// 备份失败时记下原因，一小时内不再自动重试；不会留下临时文件。
func TestBackupFailure(t *testing.T) {
	e := apptest.New(t)
	b := e.App.Backup
	if !b.Supported() {
		t.Skip("只有 SQLite 有内置备份")
	}
	now := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	b.Now = func() time.Time { return now }
	if err := e.App.Settings.SaveBackup(e.Ctx, settings.Backup{Enabled: true, Keep: 7}); err != nil {
		t.Fatal(err)
	}
	// 备份目录的位置被一个普通文件占了。
	if err := os.WriteFile(b.Dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := b.Run(e.Ctx, false)
	if err == nil || st.LastError == "" || st.LastOKAt != 0 {
		t.Fatalf("应记下失败：%+v %v", st, err)
	}
	if err := os.Remove(b.Dir); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	if st, _ = b.Run(e.Ctx, false); st.LastOKAt != 0 {
		t.Errorf("失败后一小时内不应自动重试：%+v", st)
	}
	now = now.Add(time.Hour)
	if st, err = b.Run(e.Ctx, false); err != nil || st.LastError != "" || st.LastOKAt == 0 {
		t.Errorf("过了一小时应重试成功：%+v %v", st, err)
	}
	entries, _ := os.ReadDir(b.Dir)
	if len(entries) != 1 {
		t.Errorf("备份目录里应只有一份备份，实际 %d 个文件", len(entries))
	}
}
