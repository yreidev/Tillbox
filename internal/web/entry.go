package web

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"tillbox/internal/admin"
	"tillbox/internal/settings"
)

type entrySettings interface {
	Entry(ctx context.Context) (settings.Entry, error)
}

// gated 判断路径是不是后台：后台页面、登录页和它们用的接口。
func gated(p string) bool {
	return p == "/login" || p == "/admin" || strings.HasPrefix(p, "/admin/") ||
		strings.HasPrefix(p, "/web-api/admin/") || strings.HasPrefix(p, "/web-api/auth/")
}

// entryGate 是后台安全入口：开着时，没打开过入口链接（/admin?entry=<密钥>）的浏览器访问后台，
// 得到的和不存在的路径一样是 404，看不出有后台，也试不了密码。打开入口链接后记一个 cookie，
// 再跳回去掉密钥的地址，免得密钥留在地址栏和历史记录里。
func entryGate(next http.Handler, st entrySettings, secure func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean("/" + r.URL.Path)
		if !gated(p) {
			next.ServeHTTP(w, r)
			return
		}
		e, err := st.Entry(r.Context())
		if err != nil {
			slog.Error("读取后台安全入口设置失败", "err", err)
			http.Error(w, "服务器出错", http.StatusInternalServerError)
			return
		}
		if e.Key == "" {
			next.ServeHTTP(w, r)
			return
		}
		q := r.URL.Query()
		if key := q.Get(admin.EntryParam); key != "" && r.Method == http.MethodGet && (p == "/admin" || p == "/login") &&
			subtle.ConstantTimeCompare([]byte(key), []byte(e.Key)) == 1 {
			admin.SetEntryCookie(w, e.Key, secure(r))
			q.Del(admin.EntryParam)
			target := p
			if len(q) > 0 {
				target += "?" + q.Encode()
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		if c, err := r.Cookie(admin.EntryCookie); err == nil &&
			subtle.ConstantTimeCompare([]byte(c.Value), []byte(admin.EntryToken(e.Key))) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(p, "/web-api/") {
			apiNotFound(w)
			return
		}
		http.NotFound(w, r)
	})
}
