package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
	"github.com/uptrace/bun"
)

func TestUpgradeExistingSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "production-v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(migrations, "migrations/sqlite/00001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, fstest.MapFS{"00001_init.sql": &fstest.MapFile{Data: raw}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO settings(key,value,updated_at) VALUES('upgrade-evidence','preserve-me',1)"); err != nil {
		t.Fatal(err)
	}
	// A pre-upgrade real order must gain is_test=false without rewriting it.
	_, err = db.Exec(`INSERT INTO merchants(id,name,md5_key,created_at,updated_at) VALUES(1001,'existing','encrypted',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO orders(trade_no,merchant_id,out_trade_no,version,channel,pay_type,name,money,amount,pay_amount,notify_url,status,created_at,expires_at,released_at,updated_at) VALUES('existing',1001,'existing',1,'alipay_qr','alipay','existing','1.00',100,101,'https://example.com','paid',1,2,3,4)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	upgraded, err := Open(ctx, "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var row Setting
	if err := upgraded.NewSelect().Model(&row).Where("key = ?", "upgrade-evidence").Scan(ctx); err != nil || row.Value != "preserve-me" {
		t.Fatalf("setting lost: %v", err)
	}
	var order Order
	if err := upgraded.NewSelect().Model(&order).Where("trade_no = ?", "existing").Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if order.IsTest || order.PayAmount != 101 || order.Status != "paid" {
		t.Fatalf("old order changed: %+v", order)
	}
}

// upgradeFrom3 用 00001–00003 建一个旧库，执行 seed 写入旧数据，然后用 Open 升级到最新。
func upgradeFrom3(t *testing.T, driver, dsn string, seed func(db *sql.DB)) *bun.DB {
	t.Helper()
	return upgradeFrom(t, driver, dsn, 3, seed)
}

// upgradeFrom 先只执行前 n 个迁移，seed 写入旧版本的数据，再打开数据库执行其余迁移。
func upgradeFrom(t *testing.T, driver, dsn string, n int, seed func(db *sql.DB)) *bun.DB {
	t.Helper()
	ctx := context.Background()
	drv, dia, dir := "sqlite", goose.DialectSQLite3, "migrations/sqlite"
	if driver == "postgres" {
		drv, dia, dir = "pgx", goose.DialectPostgres, "migrations/postgres"
	}
	db, err := sql.Open(drv, dsn)
	if err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(migrations, dir+"/*.sql")
	if err != nil || len(names) < n {
		t.Fatalf("迁移文件：%v %v", err, names)
	}
	old := fstest.MapFS{}
	for _, path := range names[:n] {
		name := filepath.Base(path)
		raw, err := fs.ReadFile(migrations, path)
		if err != nil {
			t.Fatal(err)
		}
		old[name] = &fstest.MapFile{Data: raw}
	}
	provider, err := goose.NewProvider(dia, db, old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	seed(db)
	db.Close()
	upgraded, err := Open(ctx, driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upgraded.Close() })
	return upgraded
}

func exec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

func checkChannelUpgrade(t *testing.T, driver, dsn string) {
	ctx := context.Background()
	const alipay = `{"enabled":true,"app_id":"2021000000000001","app_private_key":"sealed-private-key","app_public_key":"pub","alipay_public_key":"alipub","gateway":"https://openapi.alipay.com","qrcode_url":"https://qr.alipay.com/fkx1","payee_name":"小店"}`
	db := upgradeFrom3(t, driver, dsn, func(db *sql.DB) {
		exec(t, db, `INSERT INTO settings(key,value,updated_at) VALUES('alipay','`+alipay+`',1)`)
		exec(t, db, `INSERT INTO settings(key,value,updated_at) VALUES('ledger_state','{"last_end":123}',1)`)
		exec(t, db, `INSERT INTO merchants(id,name,md5_key,created_at,updated_at) VALUES(1001,'a','k',1,1)`)
		exec(t, db, `INSERT INTO merchants(id,name,md5_key,created_at,updated_at) VALUES(1002,'b','k',1,1)`)
		exec(t, db, `INSERT INTO orders(trade_no,merchant_id,out_trade_no,version,channel,pay_type,name,money,amount,pay_amount,notify_url,status,created_at,expires_at,released_at,updated_at) VALUES('t1',1001,'o1',1,'alipay_qr','alipay','x','1.00',100,101,'https://example.com','paid',1,2,3,4)`)
		exec(t, db, `INSERT INTO transactions(channel,external_id,direction,amount,occurred_at,fingerprint,state,created_at,updated_at) VALUES('alipay_qr','e1','in',101,1,'f','matched',1,1)`)
	})
	var chs []Channel
	if err := db.NewSelect().Model(&chs).Order("id ASC").Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if len(chs) != 1 || chs[0].ID != 1 || chs[0].Type != ChannelAlipayQR || !chs[0].Enabled || chs[0].Name == "" || chs[0].CreatedAt < 1_700_000_000_000 {
		t.Fatalf("支付宝设置应变成 1 号经营码渠道：%+v", chs)
	}
	var cfg, want map[string]any
	_ = json.Unmarshal([]byte(chs[0].Config), &cfg)
	_ = json.Unmarshal([]byte(alipay), &want)
	if cfg["app_private_key"] != "sealed-private-key" || cfg["qrcode_url"] != want["qrcode_url"] || cfg["app_id"] != want["app_id"] {
		t.Errorf("渠道配置应原样沿用：%v", cfg)
	}
	var o Order
	var tx Transaction
	if err := db.NewSelect().Model(&o).Where("trade_no = 't1'").Scan(ctx); err != nil || o.Channel != "ch1" {
		t.Errorf("历史订单应指向 1 号渠道：%v %q", err, o.Channel)
	}
	if err := db.NewSelect().Model(&tx).Where("external_id = 'e1'").Scan(ctx); err != nil || tx.Channel != "ch1" {
		t.Errorf("历史流水应指向 1 号渠道：%v %q", err, tx.Channel)
	}
	if got := routesOf(t, db); !slices.Equal(got, []string{"1001:alipay=1", "1002:alipay=1"}) {
		t.Errorf("商户的支付宝应走 1 号渠道：%v", got)
	}
	var keys []string
	if err := db.NewSelect().Model((*Setting)(nil)).Column("key").Order("key").Scan(ctx, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "ledger_state:ch1" {
		t.Errorf("查账进度应改名、旧支付宝设置应删除：%v", keys)
	}
	// 新渠道的编号接在后面。
	row := &Channel{Type: ChannelAlipayQR, Name: "新渠道", Config: "{}", CreatedAt: 1, UpdatedAt: 1}
	if _, err := db.NewInsert().Model(row).Exec(ctx); err != nil || row.ID != 2 {
		t.Errorf("新渠道编号：%v %d", err, row.ID)
	}
}

func TestUpgradeToChannelsSQLite(t *testing.T) {
	checkChannelUpgrade(t, "sqlite", filepath.Join(t.TempDir(), "v3.db"))
}

func TestUpgradeToChannelsPostgres(t *testing.T) {
	dsn := os.Getenv("TILLBOX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("没有设置 TILLBOX_TEST_PG_DSN")
	}
	checkChannelUpgrade(t, "postgres", pgSchema(t, dsn, "t_upgrade_channels"))
}

// pgSchema 在测试库里建一个空的 schema，返回指向它的连接串；测试结束时删掉。
func pgSchema(t *testing.T, dsn, schema string) string {
	t.Helper()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	_, _ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE") })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// routesOf 读出全部商户的收款方式，写成“商户号:支付方式=渠道编号”，按商户号和支付方式排好。
func routesOf(t *testing.T, db *bun.DB) []string {
	t.Helper()
	var rs []MerchantRoute
	if err := db.NewSelect().Model(&rs).Order("merchant_id", "pay_type").Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = fmt.Sprintf("%d:%s=%d", r.MerchantID, r.PayType, r.ChannelID)
	}
	return out
}

// 商户从只选一个渠道换成按支付方式选：经营码和演示渠道换成支付宝，第三方易支付换成它勾选的方式。
func checkRoutesUpgrade(t *testing.T, driver, dsn string) {
	db := upgradeFrom(t, driver, dsn, 6, func(db *sql.DB) {
		exec(t, db, `INSERT INTO channels(id,type,name,enabled,config,created_at,updated_at) VALUES
			(1,'alipay_qr','经营码',TRUE,'{}',1,1),
			(2,'epay','第三方',TRUE,'{"api_url":"https://pay.example.com","types":["alipay","wxpay"]}',1,1),
			(3,'demo','演示',FALSE,'{}',1,1),
			(4,'epay','没勾方式',TRUE,'{"types":null}',1,1)`)
		for id, ch := range map[int]int{1001: 1, 1002: 2, 1003: 3, 1004: 4, 1005: 0, 1006: 99} {
			exec(t, db, fmt.Sprintf(`INSERT INTO merchants(id,name,md5_key,channel_id,created_at,updated_at) VALUES(%d,'m','k',%d,1,1)`, id, ch))
		}
	})
	want := []string{"1001:alipay=1", "1002:alipay=2", "1002:wxpay=2", "1003:alipay=3"}
	if got := routesOf(t, db); !slices.Equal(got, want) {
		t.Errorf("收款方式 %v，应为 %v", got, want)
	}
	if _, err := db.NewRaw("SELECT channel_id FROM merchants").Exec(context.Background()); err == nil {
		t.Error("merchants.channel_id 应该删掉")
	}
}

func TestUpgradeToRoutesSQLite(t *testing.T) {
	checkRoutesUpgrade(t, "sqlite", filepath.Join(t.TempDir(), "v6.db"))
}

func TestUpgradeToRoutesPostgres(t *testing.T) {
	dsn := os.Getenv("TILLBOX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("没有设置 TILLBOX_TEST_PG_DSN")
	}
	checkRoutesUpgrade(t, "postgres", pgSchema(t, dsn, "t_upgrade_routes"))
}

// 全新安装没有支付宝设置时不建渠道；演示库里的假渠道订单归到演示渠道。
func TestUpgradeDemoOrders(t *testing.T) {
	ctx := context.Background()
	db := upgradeFrom3(t, "sqlite", filepath.Join(t.TempDir(), "demo.db"), func(db *sql.DB) {
		exec(t, db, `INSERT INTO merchants(id,name,md5_key,created_at,updated_at) VALUES(1001,'a','k',1,1)`)
		exec(t, db, `INSERT INTO orders(trade_no,merchant_id,out_trade_no,version,channel,pay_type,name,money,amount,pay_amount,notify_url,status,created_at,expires_at,released_at,updated_at) VALUES('t1',1001,'o1',1,'fake','alipay','x','1.00',100,101,'https://example.com','paid',1,2,3,4)`)
		exec(t, db, `INSERT INTO settings(key,value,updated_at) VALUES('ledger_state','{}',1)`)
	})
	var chs []Channel
	if err := db.NewSelect().Model(&chs).Scan(ctx); err != nil || len(chs) != 1 || chs[0].Type != ChannelDemo || chs[0].ID != 1 {
		t.Fatalf("应建一个演示渠道：%v %+v", err, chs)
	}
	var o Order
	if err := db.NewSelect().Model(&o).Where("trade_no = 't1'").Scan(ctx); err != nil || o.Channel != "ch1" {
		t.Errorf("演示订单应指向演示渠道：%v %q", err, o.Channel)
	}
	if n, _ := db.NewSelect().Model((*Setting)(nil)).Count(ctx); n != 0 {
		t.Error("演示渠道的旧查账进度应丢弃")
	}

	// 演示库里存过没启用的支付宝设置：商户用启用的演示渠道。
	mixed := upgradeFrom3(t, "sqlite", filepath.Join(t.TempDir(), "mixed.db"), func(db *sql.DB) {
		exec(t, db, `INSERT INTO merchants(id,name,md5_key,created_at,updated_at) VALUES(1001,'a','k',1,1)`)
		exec(t, db, `INSERT INTO settings(key,value,updated_at) VALUES('alipay','{"enabled":false,"app_id":"2021"}',1)`)
		exec(t, db, `INSERT INTO orders(trade_no,merchant_id,out_trade_no,version,channel,pay_type,name,money,amount,pay_amount,notify_url,status,created_at,expires_at,released_at,updated_at) VALUES('t1',1001,'o1',1,'fake','alipay','x','1.00',100,101,'https://example.com','paid',1,2,3,4)`)
	})
	if got := routesOf(t, mixed); !slices.Equal(got, []string{"1001:alipay=2"}) {
		t.Errorf("商户的支付宝应走启用的演示渠道：%v", got)
	}

	empty := upgradeFrom3(t, "sqlite", filepath.Join(t.TempDir(), "empty.db"), func(*sql.DB) {})
	if n, _ := empty.NewSelect().Model((*Channel)(nil)).Count(ctx); n != 0 {
		t.Error("全新安装不该自动建渠道")
	}
}

// 升级指引按迁移编号判断新版本有没有数据库变更，只看 SQLite 的迁移，两种数据库的编号要一致。
func TestSchemaVersion(t *testing.T) {
	names := func(dir string) []string {
		entries, err := fs.ReadDir(migrations, dir)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}
	sqlite, pg := MaxMigration(names("migrations/sqlite")), MaxMigration(names("migrations/postgres"))
	if sqlite == 0 || sqlite != pg || SchemaVersion() != sqlite {
		t.Errorf("迁移编号：SQLite %d，PostgreSQL %d，SchemaVersion %d", sqlite, pg, SchemaVersion())
	}
	if got := MaxMigration([]string{"00002_a.sql", "00010_b.sql", "README.md", "x_y.sql"}); got != 10 {
		t.Errorf("MaxMigration = %d，应为 10", got)
	}
}
