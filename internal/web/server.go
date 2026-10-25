// Package web 把各部分组装成一个 HTTP 服务：易支付接口、后台接口、收银台接口和前端页面。
package web

import (
	"context"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"tillbox/internal/admin"
	"tillbox/internal/assistance"
	"tillbox/internal/channel/registry"
	"tillbox/internal/config"
	"tillbox/internal/confirm"
	"tillbox/internal/gateway"
	"tillbox/internal/ledger"
	"tillbox/internal/merchant"
	"tillbox/internal/notify"
	"tillbox/internal/order"
	"tillbox/internal/settings"
)

// Deps 是组装服务要用到的东西。
type Deps struct {
	Assistance *assistance.Service
	Config     *config.Config
	Gateway    *gateway.Server
	Admin      *admin.API
	Orders     *order.Service
	Merchants  *merchant.Service
	Settings   *settings.Service
	Channels   *registry.Registry
	Pollers    *ledger.Pollers
	Confirm    *confirm.Service
	Signer     *notify.Signer
	Demo       bool // TILLBOX_DEMO=true：提供模拟付款接口
	UI         fs.FS
}

// Proxy 按可信代理设置解析真实 IP 和协议。
type Proxy struct {
	Trusted   []netip.Prefix
	PublicURL string
}

func (p *Proxy) trusted(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, pre := range p.Trusted {
		if pre.Contains(addr) {
			return true
		}
	}
	return false
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, _ := netip.ParseAddr(host)
	return a.Unmap()
}

// ClientIP 返回真实 IP：直连地址是可信代理时，从 X-Forwarded-For 从右往左取第一个不可信的地址。
func (p *Proxy) ClientIP(r *http.Request) string {
	ip := remoteAddr(r)
	if !p.trusted(ip) {
		return ip.String()
	}
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff == "" {
		if xr, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
			return xr.Unmap().String()
		}
		return ip.String()
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !p.trusted(a) {
			return a.String()
		}
		ip = a
	}
	return ip.String()
}

// Secure 判断请求是不是 HTTPS（经可信代理时看 X-Forwarded-Proto）。
func (p *Proxy) Secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if p.trusted(remoteAddr(r)) {
		return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
	}
	return strings.HasPrefix(p.PublicURL, "https://")
}

// BaseURL 返回对外地址：优先用 TILLBOX_PUBLIC_URL，否则按请求推断。
func (p *Proxy) BaseURL(r *http.Request) string {
	if p.PublicURL != "" {
		return p.PublicURL
	}
	scheme := "http"
	if p.Secure(r) {
		scheme = "https"
	}
	host := r.Host
	if p.trusted(remoteAddr(r)) {
		if h := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); h != "" {
			host = h
		}
	}
	return scheme + "://" + host
}

// Handler 组装所有路由。
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()
	d.Gateway.Register(mux)
	d.Admin.Register(mux)
	registerSelfService(mux, d)
	c := &cashier{
		orders: d.Orders, merchants: d.Merchants, settings: d.Settings, channels: d.Channels, pollers: d.Pollers,
		confirm: d.Confirm, signer: d.Signer, db: d.Admin.DB, baseURL: d.Gateway.BaseURL, clientIP: d.Admin.ClientIP,
	}
	c.register(mux)
	mux.HandleFunc("/web-api/channels/{id}/notify", channelNotify(d.Confirm))
	if d.Demo {
		registerDev(mux, d)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.Admin.DB.PingContext(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/web-api/", func(w http.ResponseWriter, r *http.Request) { apiNotFound(w) })
	mux.Handle("/", spa(d.UI, d.Settings))
	return headers(entryGate(mux, d.Settings, d.Admin.Secure))
}

func apiNotFound(w http.ResponseWriter) {
	http.Error(w, `{"error":"接口不存在"}`, http.StatusNotFound)
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
