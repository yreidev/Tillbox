// Package backup 给 SQLite 数据库做自动备份：用 VACUUM INTO 在数据目录 backups/ 下生成一份完整副本，
// 校验通过后改成正式文件名，只留最近几份。PostgreSQL 不做，用数据库自己的备份工具。
//
// 备份里的密钥是用主密钥加密的，恢复时要配同一个主密钥（master.key 或 TILLBOX_MASTER_KEY），主密钥要另外保存。
package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	_ "modernc.org/sqlite" // 校验备份时单独打开备份文件

	"tillbox/internal/settings"
)

// ErrUnsupported 是数据库不是 SQLite，不能用内置备份。
var ErrUnsupported = errors.New("内置备份只支持 SQLite；PostgreSQL 请用 pg_dump 等数据库自己的备份工具")

const stateKey = "backup_state"

// 每天一份；按小时检查，所以允许提前一点，免得每天往后推一小时。
const (
	interval   = 23 * time.Hour
	retryAfter = time.Hour // 失败后隔多久再试
)

var namePattern = regexp.MustCompile(`^tillbox-\d{8}T\d{6}Z\.db$`)

// State 是备份任务的运行情况，存在设置表里。
type State struct {
	LastRunAt int64  `json:"last_run_at"`
	LastOKAt  int64  `json:"last_ok_at"`
	LastError string `json:"last_error"`
	LastFile  string `json:"last_file"`
}

// File 是一份备份。
type File struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"created_at"`
}

type Service struct {
	DB       *bun.DB
	Settings *settings.Service
	Dir      string
	// Now 可以在测试里替换。
	Now func() time.Time

	mu sync.Mutex
}

func New(db *bun.DB, st *settings.Service, dataDir string) *Service {
	return &Service{DB: db, Settings: st, Dir: filepath.Join(dataDir, "backups"), Now: time.Now}
}

// Supported 判断能不能用内置备份（数据库是 SQLite）。
func (s *Service) Supported() bool { return s.DB.Dialect().Name() == dialect.SQLite }

// Status 读备份任务的运行情况。命令行也会写，所以直接读库。
func (s *Service) Status(ctx context.Context) (State, error) {
	var v State
	_, err := s.Settings.Fresh(ctx, stateKey, &v)
	return v, err
}

// Run 按设置做一次备份：没开启、今天已经成功过、或刚失败过时什么都不做。force 为真时不看这些（后台“立即备份”和命令行）。
func (s *Service) Run(ctx context.Context, force bool) (State, error) {
	if !s.Supported() {
		return State{}, ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.Settings.Backup(ctx)
	if err != nil {
		return State{}, err
	}
	v, err := s.Status(ctx)
	if err != nil {
		return v, err
	}
	now := s.Now()
	if !force && (!cfg.Enabled ||
		(v.LastOKAt > 0 && now.Sub(time.UnixMilli(v.LastOKAt)) < interval) ||
		(v.LastError != "" && now.Sub(time.UnixMilli(v.LastRunAt)) < retryAfter)) {
		return v, nil
	}
	v.LastRunAt = now.UnixMilli()
	name, err := s.create(ctx, now)
	if err == nil {
		err = s.prune(cfg.Keep)
	}
	if err != nil {
		v.LastError = "备份失败：" + err.Error()
	} else {
		v.LastOKAt, v.LastError, v.LastFile = now.UnixMilli(), "", name
	}
	if saveErr := s.Settings.SaveRaw(ctx, stateKey, v); saveErr != nil && err == nil {
		err = saveErr
	}
	return v, err
}

// create 先备份到临时文件，校验通过、权限改成只有自己能读后再改成正式文件名，中途失败不会留下半截的备份。
func (s *Service) create(ctx context.Context, now time.Time) (string, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", fmt.Errorf("建备份目录失败：%w", err)
	}
	s.cleanTemp()
	name := "tillbox-" + now.UTC().Format("20060102T150405Z") + ".db"
	tmp := filepath.Join(s.Dir, "."+name+".tmp")
	if _, err := s.DB.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("生成备份失败：%w", err)
	}
	if err := check(ctx, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(s.Dir, name)); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return name, nil
}

// check 只读打开备份文件跑一遍 quick_check。
func check(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var res string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&res); err != nil {
		return fmt.Errorf("校验备份失败：%w", err)
	}
	if res != "ok" {
		return fmt.Errorf("备份校验没通过：%s", res)
	}
	return nil
}

// cleanTemp 删掉上次中断留下的临时文件。
func (s *Service) cleanTemp() {
	entries, _ := os.ReadDir(s.Dir)
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, ".tillbox-") && strings.HasSuffix(n, ".db.tmp") {
			_ = os.Remove(filepath.Join(s.Dir, n))
		}
	}
}

// prune 只留最近 keep 份。
func (s *Service) prune(keep int) error {
	files, err := s.List()
	if err != nil {
		return err
	}
	for _, f := range files[min(keep, len(files)):] {
		if err := os.Remove(filepath.Join(s.Dir, f.Name)); err != nil {
			return fmt.Errorf("删除旧备份失败：%w", err)
		}
	}
	return nil
}

// List 列出备份文件，新的在前。备份目录还没建时返回空列表。
func (s *Service) List() ([]File, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return []File{}, nil
	}
	if err != nil {
		return nil, err
	}
	files := []File{}
	for _, e := range entries {
		if e.IsDir() || !namePattern.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		at, err := time.Parse("20060102T150405Z", strings.TrimSuffix(strings.TrimPrefix(e.Name(), "tillbox-"), ".db"))
		if err != nil {
			continue
		}
		files = append(files, File{Name: e.Name(), Size: info.Size(), CreatedAt: at.UnixMilli()})
	}
	// 文件名里的时间是定长的 UTC 时刻，按名字倒序就是新的在前。
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(b.Name, a.Name) })
	return files, nil
}
