// Package app 把各个服务组装成一个完整的应用：HTTP 处理器加后台任务。命令行和集成测试共用。
package app

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/admin"
	"tillbox/internal/assistance"
	"tillbox/internal/backup"
	"tillbox/internal/channel/registry"
	"tillbox/internal/config"
	"tillbox/internal/confirm"
	"tillbox/internal/gateway"
	"tillbox/internal/ledger"
	"tillbox/internal/maintenance"
	"tillbox/internal/merchant"
	"tillbox/internal/monitor"
	"tillbox/internal/notify"
	"tillbox/internal/order"
	"tillbox/internal/secret"
	"tillbox/internal/settings"
	"tillbox/internal/update"
	"tillbox/internal/web"
)

// App 是组装好的应用。
type App struct {
	Assistance  *assistance.Service
	DB          *bun.DB
	Settings    *settings.Service
	Merchants   *merchant.Service
	Orders      *order.Service
	Ledger      *ledger.Ledger
	Channels    *registry.Registry
	Pollers     *ledger.Pollers
	Confirm     *confirm.Service
	Notifier    *notify.Worker
	Maintenance *maintenance.Service
	Backup      *backup.Service
	Monitor     *monitor.Service
	Update      *update.Service
	Signer      *notify.Signer
	Auth        *admin.Auth
	Handler     http.Handler

	// AdminPassword 是这次启动新建的管理员密码；已有管理员时为空。
	AdminPassword string

	demo bool
}

// New 组装应用：准备平台密钥、首次启动时建管理员。db 要已经迁移好。
func New(ctx context.Context, cfg *config.Config, db *bun.DB, box *secret.Box, ui fs.FS) (*App, error) {
	st := settings.New(db, box)
	if _, err := st.Platform(ctx); err != nil {
		return nil, fmt.Errorf("准备平台密钥失败：%w", err)
	}
	pw, err := admin.EnsureAdmin(ctx, db)
	if err != nil {
		return nil, err
	}

	a := &App{DB: db, Settings: st, AdminPassword: pw, demo: cfg.Demo}
	version := cfg.Version
	if version == "" {
		version = "dev"
	}
	a.Update = update.New(st, version)
	a.Merchants = merchant.New(db, box)
	a.Orders = order.New(db, st)
	a.Signer = &notify.Signer{Merchants: a.Merchants, Settings: st}
	a.Notifier = notify.NewWorker(db, a.Signer)
	a.Maintenance = maintenance.New(db, st, cfg.DataDir)
	a.Backup = backup.New(db, st, cfg.DataDir)
	a.Orders.OnPaid = func() {
		a.Notifier.Wake()
		a.Monitor.Wake()
	}

	a.Ledger = ledger.New(db, a.Orders)
	a.Pollers = ledger.NewPollers(db, a.Ledger, st)
	a.Channels = registry.New(db, box, cfg.Demo)
	a.Channels.OnChange = func() { a.Pollers.Sync(a.Channels.Sources()) }
	a.Merchants.Channels = a.Channels
	if err := a.Channels.EnsureDemo(ctx); err != nil {
		return nil, fmt.Errorf("建演示渠道失败：%w", err)
	}
	if err := a.Channels.Load(ctx); err != nil {
		return nil, fmt.Errorf("加载支付渠道失败：%w", err)
	}
	a.Confirm = confirm.New(db, st, a.Orders, a.Ledger, a.Channels)
	if err := a.Confirm.Load(ctx); err != nil {
		return nil, fmt.Errorf("读取第三方易支付渠道状态失败：%w", err)
	}
	a.Assistance = assistance.New(db, st, a.Orders, a.Ledger, a.Pollers, a.Confirm, a.Notifier)
	a.Monitor = &monitor.Service{
		DB: db, Settings: st, Telegram: monitor.NewTelegram(db, st), PublicURL: cfg.PublicURL, Now: time.Now,
		Checker: &monitor.Checker{
			DB: db, Settings: st, Channels: a.Channels, Pollers: a.Pollers, Confirm: a.Confirm,
			Maintenance: a.Maintenance, Backup: a.Backup, Assistance: a.Assistance, Now: time.Now,
		},
	}
	a.Assistance.OnClaim = a.Monitor.Wake

	proxy := &web.Proxy{Trusted: cfg.TrustedPrefixes, PublicURL: cfg.PublicURL}
	gw := &gateway.Server{
		Merchants: a.Merchants, Orders: a.Orders, Settings: st, Channels: a.Channels, Confirm: a.Confirm,
		BaseURL: proxy.BaseURL, ClientIP: proxy.ClientIP,
	}
	a.Auth = admin.NewAuth(db)
	api := &admin.API{
		Assistance: a.Assistance,
		DB:         db, Auth: a.Auth, Merchants: a.Merchants, Orders: a.Orders, Ledger: a.Ledger, Pollers: a.Pollers, Confirm: a.Confirm, Channels: a.Channels,
		Notifier: a.Notifier, Signer: a.Signer, Maintenance: a.Maintenance, Backup: a.Backup, Update: a.Update, Settings: st, Monitor: a.Monitor, ClientIP: proxy.ClientIP, Secure: proxy.Secure,
		BaseURL: proxy.BaseURL, Demo: cfg.Demo,
	}
	a.Handler = web.Handler(web.Deps{
		Assistance: a.Assistance,
		Config:     cfg, Gateway: gw, Admin: api, Orders: a.Orders, Merchants: a.Merchants, Settings: st, Channels: a.Channels,
		Pollers: a.Pollers, Confirm: a.Confirm, Signer: a.Signer, Demo: cfg.Demo, UI: ui,
	})
	return a, nil
}

// Run 跑后台任务（查账、第三方平台查单、通知、订单过期、清理会话、监控告警、归档和备份），直到 ctx 结束。
func (a *App) Run(ctx context.Context) {
	go a.Assistance.Run(ctx)
	go a.Monitor.Run(ctx)
	go every(ctx, time.Hour, func() {
		if _, err := a.Maintenance.Run(ctx, false); err != nil && ctx.Err() == nil {
			slog.Error("历史记录归档失败，原始数据保留", "err", err)
		}
	})
	if a.Backup.Supported() {
		go every(ctx, time.Hour, func() {
			if _, err := a.Backup.Run(ctx, false); err != nil && ctx.Err() == nil {
				slog.Error("自动备份失败", "err", err)
			}
		})
	}
	// 演示模式只在本机和 CI 里跑，不向 GitHub 查新版本。
	if !a.demo {
		go a.Update.Run(ctx)
	}
	go a.Pollers.Run(ctx)
	go a.Confirm.Run(ctx)
	go a.Notifier.Run(ctx)
	go every(ctx, 5*time.Second, func() {
		if _, err := a.Orders.ExpireDue(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("处理过期订单失败", "err", err)
		}
	})
	go every(ctx, time.Hour, func() {
		if err := a.Auth.CleanupSessions(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("清理过期会话失败", "err", err)
		}
	})
}

func every(ctx context.Context, d time.Duration, f func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		f()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
