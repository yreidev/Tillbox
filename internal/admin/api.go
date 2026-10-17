package admin

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/assistance"
	"tillbox/internal/channel/registry"
	"tillbox/internal/confirm"
	"tillbox/internal/ledger"
	"tillbox/internal/maintenance"
	"tillbox/internal/merchant"
	"tillbox/internal/monitor"
	"tillbox/internal/notify"
	"tillbox/internal/order"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// API 是后台接口，挂在 /web-api/ 下。
type API struct {
	Assistance  *assistance.Service
	DB          *bun.DB
	Auth        *Auth
	Merchants   *merchant.Service
	Orders      *order.Service
	Ledger      *ledger.Ledger
	Pollers     *ledger.Pollers
	Confirm     *confirm.Service
	Channels    *registry.Registry
	Notifier    *notify.Worker
	Signer      *notify.Signer
	Maintenance *maintenance.Service
	Settings    *settings.Service
	Monitor     *monitor.Service
	// ClientIP 返回请求方的真实 IP。
	ClientIP func(r *http.Request) string
	// Secure 判断请求是不是经由 HTTPS 进来的（决定 cookie 的 Secure 标记）。
	Secure func(r *http.Request) bool
	// BaseURL 返回对外地址（不带末尾的 /），后台展示给对接方填的接口地址用它。
	BaseURL func(r *http.Request) string
	// Demo 为真表示本地演示模式（TILLBOX_DEMO=true），可以用演示渠道和模拟付款。
	Demo bool
}

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /web-api/admin/assistance", a.wrap(a.assistance))
	mux.HandleFunc("PUT /web-api/admin/assistance/config", a.wrap(a.saveAssistance))
	mux.HandleFunc("POST /web-api/admin/assistance/test", a.wrap(a.testAssistance))
	mux.HandleFunc("POST /web-api/admin/assistance/run", a.wrap(a.runAssistance))
	mux.HandleFunc("GET /web-api/admin/assistance/cases/{id}", a.wrap(a.assistanceDetail))
	mux.HandleFunc("POST /web-api/admin/assistance/cases/{id}", a.wrap(a.updateAssistance))
	mux.HandleFunc("GET /web-api/admin/monitor", a.wrap(a.monitorPage))
	mux.HandleFunc("POST /web-api/admin/monitor/check", a.wrap(a.checkNow))
	mux.HandleFunc("GET /web-api/admin/monitor/trends", a.wrap(a.trends))
	mux.HandleFunc("GET /web-api/admin/operations", a.wrap(a.operations))
	mux.HandleFunc("PUT /web-api/admin/operations/retention", a.wrap(a.saveRetention))
	mux.HandleFunc("POST /web-api/admin/operations/archive", a.wrap(a.runArchive))
	mux.HandleFunc("GET /web-api/admin/operations/archives/{id}", a.wrap(a.downloadArchive))
	mux.HandleFunc("GET /web-api/payment-test/notify", a.receivePaymentTest)
	mux.HandleFunc("POST /web-api/admin/payment-tests", a.wrap(a.createPaymentTest))
	mux.HandleFunc("GET /web-api/admin/payment-tests/{id}/receipt", a.wrap(a.testReceipt))
	mux.HandleFunc("POST /web-api/auth/login", a.open(a.login))
	mux.HandleFunc("POST /web-api/auth/logout", a.open(a.logout))
	mux.HandleFunc("GET /web-api/auth/me", a.wrap(a.me))
	mux.HandleFunc("POST /web-api/auth/password", a.wrap(a.changePassword))

	mux.HandleFunc("GET /web-api/admin/overview", a.wrap(a.overview))

	mux.HandleFunc("GET /web-api/admin/channels", a.wrap(a.listChannels))
	mux.HandleFunc("POST /web-api/admin/channels", a.wrap(a.createChannel))
	mux.HandleFunc("PUT /web-api/admin/channels/{id}", a.wrap(a.updateChannel))
	mux.HandleFunc("DELETE /web-api/admin/channels/{id}", a.wrap(a.deleteChannel))
	mux.HandleFunc("POST /web-api/admin/channels/{id}/test", a.wrap(a.testChannel))
	mux.HandleFunc("POST /web-api/admin/channels/{id}/keypair", a.wrap(a.channelKeypair))

	mux.HandleFunc("GET /web-api/admin/merchants", a.wrap(a.listMerchants))
	mux.HandleFunc("POST /web-api/admin/merchants", a.wrap(a.createMerchant))
	mux.HandleFunc("PUT /web-api/admin/merchants/{id}", a.wrap(a.updateMerchant))
	mux.HandleFunc("DELETE /web-api/admin/merchants/{id}", a.wrap(a.deleteMerchant))
	mux.HandleFunc("GET /web-api/admin/merchants/{id}/key", a.wrap(a.merchantKey))
	mux.HandleFunc("POST /web-api/admin/merchants/{id}/reset-key", a.wrap(a.resetMerchantKey))

	mux.HandleFunc("GET /web-api/admin/orders", a.wrap(a.listOrders))
	mux.HandleFunc("GET /web-api/admin/orders/export", a.wrap(a.exportOrders))
	mux.HandleFunc("GET /web-api/admin/orders/{id}", a.wrap(a.getOrder))
	mux.HandleFunc("POST /web-api/admin/orders/{id}/close", a.wrap(a.closeOrder))
	mux.HandleFunc("POST /web-api/admin/orders/{id}/unlink", a.wrap(a.unlinkOrder))

	mux.HandleFunc("GET /web-api/admin/transactions", a.wrap(a.listTransactions))
	mux.HandleFunc("POST /web-api/admin/transactions/{id}/link", a.wrap(a.linkTransaction))
	mux.HandleFunc("GET /web-api/admin/anomalies", a.wrap(a.listAnomalies))
	mux.HandleFunc("POST /web-api/admin/anomalies/{id}/resolve", a.wrap(a.resolveAnomaly))
	mux.HandleFunc("GET /web-api/admin/ledger/status", a.wrap(a.ledgerStatus))
	mux.HandleFunc("POST /web-api/admin/ledger/poll", a.wrap(a.ledgerPoll))

	mux.HandleFunc("GET /web-api/admin/notifications", a.wrap(a.listNotifications))
	mux.HandleFunc("POST /web-api/admin/notifications/{id}/resend", a.wrap(a.resendNotification))

	mux.HandleFunc("GET /web-api/admin/settings", a.wrap(a.getSettings))
	mux.HandleFunc("PUT /web-api/admin/settings/payment", a.wrap(a.savePayment))
	mux.HandleFunc("POST /web-api/admin/settings/platform/regenerate", a.wrap(a.regeneratePlatform))
	mux.HandleFunc("PUT /web-api/admin/settings/site", a.wrap(a.saveSite))
	mux.HandleFunc("PUT /web-api/admin/settings/home", a.wrap(a.saveHome))
	mux.HandleFunc("POST /web-api/admin/settings/entry", a.wrap(a.resetEntry))
	mux.HandleFunc("DELETE /web-api/admin/settings/entry", a.wrap(a.disableEntry))
	mux.HandleFunc("GET /web-api/admin/settings/telegram", a.wrap(a.getTelegram))
	mux.HandleFunc("PUT /web-api/admin/settings/telegram", a.wrap(a.saveTelegram))
	mux.HandleFunc("DELETE /web-api/admin/settings/telegram", a.wrap(a.clearTelegram))
	mux.HandleFunc("POST /web-api/admin/settings/telegram/bind", a.wrap(a.bindTelegram))
	mux.HandleFunc("DELETE /web-api/admin/settings/telegram/bind", a.wrap(a.cancelBindTelegram))
	mux.HandleFunc("POST /web-api/admin/settings/telegram/test", a.wrap(a.testTelegram))
	mux.HandleFunc("DELETE /web-api/admin/settings/telegram/chats/{id}", a.wrap(a.unbindTelegram))

	mux.HandleFunc("GET /web-api/admin/audit", a.wrap(a.listAudit))
}

func (a *API) actor(r *http.Request) string {
	if Current(r.Context()) != nil {
		return Actor
	}
	return "?"
}

func (a *API) login(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	token, _, err := a.Auth.Login(r.Context(), in.Password, a.ClientIP(r), r.UserAgent())
	switch {
	case err == nil:
	case errors.Is(err, ErrBadLogin):
		return &httpError{http.StatusUnauthorized, err.Error()}
	case errors.Is(err, ErrTooManyTries):
		return &httpError{http.StatusTooManyRequests, err.Error()}
	default:
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: a.Secure(r),
		SameSite: http.SameSiteStrictMode, MaxAge: int(SessionTTL / time.Second),
	})
	ok(w, map[string]any{})
	return nil
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(CookieName); err == nil {
		_ = a.Auth.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: a.Secure(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	ok(w, map[string]any{})
	return nil
}

func (a *API) me(w http.ResponseWriter, r *http.Request) error {
	site, err := a.Settings.Site(r.Context())
	if err != nil {
		return err
	}
	ok(w, map[string]any{"site_name": site.Name, "demo": a.Demo, "api_url": a.BaseURL(r) + "/"})
	return nil
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	c, _ := r.Cookie(CookieName)
	if err := a.Auth.ChangePassword(r.Context(), Current(r.Context()), in.Old, in.New, c.Value); err != nil {
		if strings.HasPrefix(err.Error(), "原密码") || strings.HasPrefix(err.Error(), "密码") {
			return badRequest(err.Error())
		}
		return err
	}
	ok(w, map[string]any{})
	return nil
}

func (a *API) listAudit(w http.ResponseWriter, r *http.Request) error {
	limit, offset := page(r)
	var logs []store.AuditLog
	total, err := a.DB.NewSelect().Model(&logs).Order("id DESC").Limit(int64(limit)).Offset(int64(offset)).ScanAndCount(r.Context())
	if err != nil {
		return err
	}
	ok(w, map[string]any{"items": logs, "total": total})
	return nil
}
