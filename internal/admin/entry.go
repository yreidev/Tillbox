package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"tillbox/internal/settings"
	"tillbox/internal/store"
)

const (
	// EntryCookie 记着这个浏览器打开过后台安全入口。值是密钥的摘要，换密钥后旧的自动失效。
	// 用 Lax：从书签或密码管理器打开入口链接再跳到登录页时，Strict 的 cookie 会被浏览器扣下。
	EntryCookie = "tillbox_entry"
	// EntryParam 是入口链接里带密钥的参数：/admin?entry=<密钥>
	EntryParam = "entry"
	entryTTL   = 400 * 24 * time.Hour
)

// EntryToken 是入口 cookie 的值。
func EntryToken(key string) string {
	sum := sha256.Sum256([]byte("tillbox-entry:" + key))
	return hex.EncodeToString(sum[:])
}

// EntryURL 是入口链接，base 是对外地址（不带末尾的 /）。
func EntryURL(base, key string) string {
	return strings.TrimRight(base, "/") + "/admin?" + EntryParam + "=" + key
}

// SetEntryCookie 让这个浏览器以后不用再打开入口链接。
func SetEntryCookie(w http.ResponseWriter, key string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: EntryCookie, Value: EntryToken(key), Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(entryTTL / time.Second),
	})
}

func (a *API) entryView(r *http.Request) (map[string]any, error) {
	e, err := a.Settings.Entry(r.Context())
	if err != nil {
		return nil, err
	}
	if e.Key == "" {
		return map[string]any{"enabled": false, "url": ""}, nil
	}
	return map[string]any{"enabled": true, "url": EntryURL(a.BaseURL(r), e.Key)}, nil
}

func (a *API) saveHome(w http.ResponseWriter, r *http.Request) error {
	var in settings.Home
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.URL = strings.TrimSpace(in.URL)
	if err := in.Validate(); err != nil {
		return badRequest(err.Error())
	}
	if in.Mode == settings.HomeRedirect && strings.TrimRight(in.URL, "/") == a.BaseURL(r) {
		return badRequest("不能跳转到首页自己")
	}
	if err := a.Settings.SaveHome(r.Context(), in); err != nil {
		return err
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "settings.home", "", homeText[in.Mode])
	ok(w, map[string]any{"home": in})
	return nil
}

var homeText = map[string]string{
	settings.HomeHelp:     "首页改为付款人自助页",
	settings.HomePage:     "首页改为自定义页面",
	settings.HomeRedirect: "首页改为跳转到其他网址",
	settings.HomeNotFound: "首页改为 404",
}

// resetEntry 开启安全入口或换一个链接。当前浏览器顺带放行，免得把自己关在外面。
func (a *API) resetEntry(w http.ResponseWriter, r *http.Request) error {
	before, err := a.Settings.Entry(r.Context())
	if err != nil {
		return err
	}
	e, err := a.Settings.ResetEntry(r.Context())
	if err != nil {
		return err
	}
	SetEntryCookie(w, e.Key, a.Secure(r))
	detail := "开启了后台安全入口"
	if before.Key != "" {
		detail = "换了后台安全入口链接，旧链接失效"
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "settings.entry", "", detail)
	view, err := a.entryView(r)
	if err != nil {
		return err
	}
	ok(w, map[string]any{"entry": view})
	return nil
}

func (a *API) disableEntry(w http.ResponseWriter, r *http.Request) error {
	if err := a.Settings.DisableEntry(r.Context()); err != nil {
		return err
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "settings.entry_off", "", "关闭了后台安全入口")
	ok(w, map[string]any{"entry": map[string]any{"enabled": false, "url": ""}})
	return nil
}
