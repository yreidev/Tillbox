// Tillbox：兼容易支付协议的自托管收款系统。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/admin"
	"tillbox/internal/app"
	"tillbox/internal/config"
	"tillbox/internal/secret"
	"tillbox/internal/settings"
	"tillbox/internal/store"
	"tillbox/internal/webui"
)

// version 在构建时用 -ldflags "-X main.version=…" 写入。
var version = "dev"
var revision = "dev"

const usage = `用法：
  tillbox [serve]                 启动服务
  tillbox reset-password          重置管理员密码，新密码打印到终端
  tillbox entry                   显示后台安全入口链接
  tillbox entry reset             换一个后台安全入口链接（没开时开启），旧链接失效
  tillbox entry off               关闭后台安全入口
  tillbox version                 显示版本

配置全部走环境变量，见 docs/deploy.md。`

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "reset-password":
		err = resetPassword()
	case "entry":
		err = entry(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("tillbox", version, revision)
	case "help", "-h", "--help":
		fmt.Println(usage)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func setupLog(level string) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})))
}

func open(ctx context.Context) (*config.Config, *bun.DB, *secret.Box, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, nil, err
	}
	setupLog(cfg.LogLevel)
	key, err := secret.LoadKey(cfg.MasterKey, cfg.DataDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("读取主密钥失败：%w", err)
	}
	box, err := secret.New(key)
	if err != nil {
		return nil, nil, nil, err
	}
	db, err := store.Open(ctx, cfg.DBDriver, cfg.DBDSN)
	if err != nil {
		return nil, nil, nil, err
	}
	return cfg, db, box, nil
}

func resetPassword() error {
	ctx := context.Background()
	_, db, _, err := open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	pw, err := admin.ResetPassword(ctx, db)
	if err != nil {
		return err
	}
	fmt.Printf("已重置管理员密码：%s\n所有登录会话已失效。\n", pw)
	return nil
}

// entry 在服务器上找回、换掉或关闭后台安全入口，运行中的服务马上生效。
func entry(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	if len(args) > 1 || (sub != "" && sub != "reset" && sub != "off") {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx := context.Background()
	cfg, db, box, err := open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	st := settings.New(db, box)
	e, err := st.Entry(ctx)
	if err != nil {
		return err
	}
	switch sub {
	case "reset":
		detail := "开启了后台安全入口"
		if e.Key != "" {
			detail = "换了后台安全入口链接，旧链接失效"
		}
		if e, err = st.ResetEntry(ctx); err != nil {
			return err
		}
		_ = store.Audit(ctx, db, "命令行", "settings.entry", "", detail)
	case "off":
		if e.Key == "" {
			break
		}
		if err := st.DisableEntry(ctx); err != nil {
			return err
		}
		_ = store.Audit(ctx, db, "命令行", "settings.entry_off", "", "关闭了后台安全入口")
		fmt.Println("已关闭后台安全入口，后台和登录页恢复直接访问。")
		return nil
	}
	if e.Key == "" {
		fmt.Println("没有开启后台安全入口，后台和登录页可以直接访问。")
		return nil
	}
	base := cfg.PublicURL
	if base == "" {
		base = "https://<你的域名>"
	}
	fmt.Printf("后台安全入口：%s\n", admin.EntryURL(base, e.Key))
	return nil
}

func serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, db, box, err := open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	slog.Info("Tillbox 启动", "version", version, "revision", revision, "db", cfg.DBDriver, "demo", cfg.Demo)

	a, err := app.New(ctx, cfg, db, box, webui.FS())
	if err != nil {
		return err
	}
	if a.AdminPassword != "" {
		slog.Warn("已生成管理员密码，请登录后立即修改", "password", a.AdminPassword)
	}
	if cfg.Demo {
		slog.Warn("正在使用本地演示模式：可以建演示渠道，付款靠 POST /web-api/dev/pay 模拟，不要用于生产")
	}
	a.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("开始监听", "addr", cfg.Listen, "public_url", cfg.PublicURL)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	slog.Info("正在停止")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
