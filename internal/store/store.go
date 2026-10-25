// Package store 负责数据库连接、迁移和表模型。
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	_ "modernc.org/sqlite"
)

//go:embed migrations
var migrations embed.FS

// SchemaVersion 是这个版本带的最新数据库迁移编号。两种数据库的迁移编号一致（迁移测试里检查）。
func SchemaVersion() int {
	entries, _ := fs.ReadDir(migrations, "migrations/sqlite")
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return MaxMigration(names)
}

// MaxMigration 从 goose 迁移文件名（00006_admin_events.sql）里取最大的编号，没有时为 0。
func MaxMigration(names []string) int {
	v := 0
	for _, name := range names {
		num, _, ok := strings.Cut(name, "_")
		if n, err := strconv.Atoi(num); ok && err == nil && strings.HasSuffix(name, ".sql") {
			v = max(v, n)
		}
	}
	return v
}

// Open 连上数据库并执行迁移。
func Open(ctx context.Context, driver, dsn string) (*bun.DB, error) {
	var (
		sqldb *sql.DB
		db    *bun.DB
		err   error
		dir   string
		dia   goose.Dialect
	)
	switch driver {
	case "sqlite":
		if err := os.MkdirAll(filepath.Dir(dsn), 0o700); err != nil {
			return nil, err
		}
		sqldb, err = sql.Open("sqlite", "file:"+dsn+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_txlock=immediate")
		if err != nil {
			return nil, err
		}
		// WAL 下读写可以并发；显式事务用 BEGIN IMMEDIATE（_txlock），写锁在开头就拿，不会出现锁升级冲突。
		// 不限成一个连接：万一哪里在事务里又用了外面的连接，最多等 busy_timeout 后报错，而不是整个服务卡死。
		sqldb.SetMaxOpenConns(4)
		db = bun.NewDB(sqldb, sqlitedialect.New())
		dir, dia = "migrations/sqlite", goose.DialectSQLite3
	case "postgres":
		sqldb, err = sql.Open("pgx", dsn)
		if err != nil {
			return nil, err
		}
		sqldb.SetMaxOpenConns(10)
		sqldb.SetConnMaxIdleTime(5 * time.Minute)
		db = bun.NewDB(sqldb, pgdialect.New())
		dir, dia = "migrations/postgres", goose.DialectPostgres
	default:
		return nil, fmt.Errorf("不支持的数据库 %q", driver)
	}
	if err := sqldb.PingContext(ctx); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("连接数据库失败：%w", err)
	}
	sub, err := fs.Sub(migrations, dir)
	if err != nil {
		sqldb.Close()
		return nil, err
	}
	provider, err := goose.NewProvider(dia, sqldb, sub)
	if err != nil {
		sqldb.Close()
		return nil, err
	}
	if _, err := provider.Up(ctx); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("数据库迁移失败：%w", err)
	}
	return db, nil
}

// Now 返回当前 Unix 毫秒，库里所有时间都用这个单位。
func Now() int64 { return time.Now().UnixMilli() }

// ErrConflict 是保存时发现记录在载入之后已经在别处改过（另一个标签页、另一个管理员或命令行）。
// 后台接口把它转成 409，页面保留草稿，让管理员选择载入最新还是覆盖。
var ErrConflict = errors.New("在你打开之后已经在别处改过")

// Revise 是记录改动后的修改时间：一般是现在，但一定比原来的大，同一毫秒里连改两次版本也会变。
func Revise(old int64) int64 { return max(Now(), old+1) }

// Ms 把时间转成 Unix 毫秒。
func Ms(t time.Time) int64 { return t.UnixMilli() }

// Time 把 Unix 毫秒转回时间；0 返回零值。
func Time(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// QueueAdminEvent 排一条发给管理员的业务消息，同一个对象只排一次。
func QueueAdminEvent(ctx context.Context, db bun.IDB, kind string, refID int64) error {
	_, err := db.NewInsert().Model(&AdminEvent{Kind: kind, RefID: refID, Status: AdminEventPending, CreatedAt: Now()}).
		On("CONFLICT (kind, ref_id) DO NOTHING").Exec(ctx)
	return err
}

// Audit 记一条操作日志。
func Audit(ctx context.Context, db bun.IDB, actor, action, target, detail string) error {
	_, err := db.NewInsert().Model(&AuditLog{
		Actor: actor, Action: action, Target: target, Detail: detail, CreatedAt: Now(),
	}).Exec(ctx)
	return err
}
