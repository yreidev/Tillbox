// Package store 负责数据库连接、迁移和表模型。
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// Ms 把时间转成 Unix 毫秒。
func Ms(t time.Time) int64 { return t.UnixMilli() }

// Time 把 Unix 毫秒转回时间；0 返回零值。
func Time(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// Audit 记一条操作日志。
func Audit(ctx context.Context, db bun.IDB, actor, action, target, detail string) error {
	_, err := db.NewInsert().Model(&AuditLog{
		Actor: actor, Action: action, Target: target, Detail: detail, CreatedAt: Now(),
	}).Exec(ctx)
	return err
}
