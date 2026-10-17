package web

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"

	"tillbox/internal/admin"
	"tillbox/internal/settings"
)

func request(remote string, header map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://pay.internal/x", nil)
	r.RemoteAddr = remote
	for k, v := range header {
		r.Header.Set(k, v)
	}
	return r
}

func TestClientIP(t *testing.T) {
	p := &Proxy{Trusted: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}}
	cases := []struct {
		remote string
		header map[string]string
		want   string
	}{
		// 直连的不可信地址：不看请求头。
		{"203.0.113.5:1234", map[string]string{"X-Forwarded-For": "1.1.1.1"}, "203.0.113.5"},
		// 可信代理：从右往左取第一个不可信的。
		{"10.0.0.2:80", map[string]string{"X-Forwarded-For": "1.1.1.1, 198.51.100.7, 10.0.0.9"}, "198.51.100.7"},
		// 伪造的最左边地址不会被采信。
		{"10.0.0.2:80", map[string]string{"X-Forwarded-For": "6.6.6.6,198.51.100.7"}, "198.51.100.7"},
		// 全是可信代理：取最左边那个。
		{"10.0.0.2:80", map[string]string{"X-Forwarded-For": "10.1.1.1, 10.2.2.2"}, "10.1.1.1"},
		// 中间有坏值就停在它右边。
		{"10.0.0.2:80", map[string]string{"X-Forwarded-For": "1.1.1.1, garbage, 10.3.3.3"}, "10.3.3.3"},
		{"10.0.0.2:80", map[string]string{"X-Real-IP": "192.0.2.1"}, "192.0.2.1"},
		{"10.0.0.2:80", map[string]string{"X-Real-IP": "nope"}, "10.0.0.2"},
		{"10.0.0.2:80", nil, "10.0.0.2"},
		// IPv4 映射的 IPv6 地址按 IPv4 处理。
		{"[::ffff:10.0.0.2]:80", map[string]string{"X-Forwarded-For": "::ffff:203.0.113.1"}, "203.0.113.1"},
		{"[::1]:80", map[string]string{"X-Forwarded-For": "2001:db8::1"}, "2001:db8::1"},
	}
	for _, c := range cases {
		if got := p.ClientIP(request(c.remote, c.header)); got != c.want {
			t.Errorf("%s %v：%s，应为 %s", c.remote, c.header, got, c.want)
		}
	}
	none := &Proxy{}
	if got := none.ClientIP(request("10.0.0.2:80", map[string]string{"X-Forwarded-For": "1.1.1.1"})); got != "10.0.0.2" {
		t.Errorf("没配可信代理时不该看请求头：%s", got)
	}
}

func TestSecureAndBaseURL(t *testing.T) {
	p := &Proxy{Trusted: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	viaProxy := request("10.0.0.2:80", map[string]string{"X-Forwarded-Proto": "https, http", "X-Forwarded-Host": "pay.example.com, x"})
	if !p.Secure(viaProxy) || p.BaseURL(viaProxy) != "https://pay.example.com" {
		t.Errorf("经可信代理：%v %s", p.Secure(viaProxy), p.BaseURL(viaProxy))
	}
	spoofed := request("203.0.113.5:80", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "evil.example.com"})
	if p.Secure(spoofed) || p.BaseURL(spoofed) != "http://pay.internal" {
		t.Errorf("不可信来源的转发头不该采信：%v %s", p.Secure(spoofed), p.BaseURL(spoofed))
	}
	direct := request("203.0.113.5:80", nil)
	direct.TLS = &tls.ConnectionState{}
	if !p.Secure(direct) || p.BaseURL(direct) != "https://pay.internal" {
		t.Error("直连 TLS 应算 HTTPS")
	}
	fixed := &Proxy{PublicURL: "https://pay.example.com"}
	if fixed.BaseURL(spoofed) != "https://pay.example.com" || !fixed.Secure(spoofed) {
		t.Error("配置了对外地址时以它为准")
	}
}

type fakeSite struct {
	site  settings.Site
	home  settings.Home
	entry settings.Entry
}

func (f *fakeSite) Site(context.Context) (settings.Site, error)   { return f.site, nil }
func (f *fakeSite) Home(context.Context) (settings.Home, error)   { return f.home, nil }
func (f *fakeSite) Entry(context.Context) (settings.Entry, error) { return f.entry, nil }

var testUI = fstest.MapFS{
	"index.html":        {Data: []byte(`<!doctype html><meta name="application-name" content="Tillbox" /><title>Tillbox</title>`)},
	"manifest.json":     {Data: []byte(`{"name":"Tillbox","short_name":"Tillbox","display":"standalone"}`)},
	"assets/app-1.js":   {Data: []byte("console.log(1)")},
	"favicon.svg":       {Data: []byte("<svg/>")},
	"assets/zxing.wasm": {Data: []byte{0, 'a', 's', 'm'}},
}

func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestSPA(t *testing.T) {
	site := &fakeSite{site: settings.Site{Name: "站<点>"}, home: settings.Home{Mode: settings.HomeHelp}}
	h := spa(testUI, site)
	get := func(method, path string) *httptest.ResponseRecorder { return serve(h, method, path) }
	if w := get("GET", "/assets/app-1.js"); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("静态资源：%d %v", w.Code, w.Header())
	}
	if w := get("GET", "/assets/zxing.wasm"); w.Code != 200 || w.Header().Get("Content-Type") != "application/wasm" {
		t.Errorf("wasm 的类型：%v", w.Header())
	}
	if w := get("GET", "/favicon.svg"); w.Code != 200 || w.Header().Get("Cache-Control") != "" {
		t.Errorf("根目录文件不该长缓存：%v", w.Header())
	}
	// 只有前端页面回退到 index.html，其余路径和不存在的文件一样 404
	for _, p := range []string{"/assets/missing.js", "/missing.png", "/missing", "/cashier", "/admin-x", "/helpme", "/assets", "/wp-login.php", "/../../etc/passwd"} {
		if w := get("GET", p); w.Code != 404 || strings.Contains(w.Body.String(), "<title>") {
			t.Errorf("%s 应 404：%d %s", p, w.Code, w.Body)
		}
	}
	for _, p := range []string{"/", "/index.html", "/cashier/2026101500000000001", "/admin", "/admin/orders", "/login", "/help"} {
		w := get("GET", p)
		body := w.Body.String()
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s 应回退到 index.html：%d %s", p, w.Code, body)
		}
		// 标题和应用名换成站点名
		if !strings.Contains(body, "<title>站&lt;点&gt;</title>") || !strings.Contains(body, `<meta name="application-name" content="站&lt;点&gt;"`) || strings.Contains(body, "Tillbox") {
			t.Errorf("%s 的标题：%s", p, body)
		}
		framed := w.Header().Get("X-Frame-Options") == "DENY"
		if want := strings.HasPrefix(p, "/admin") || p == "/login"; framed != want {
			t.Errorf("%s 的 X-Frame-Options：%v", p, w.Header())
		}
	}
	w := get("GET", "/manifest.json")
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m["name"] != "站<点>" || m["short_name"] != "站<点>" || m["display"] != "standalone" {
		t.Errorf("清单里的名字：%s", w.Body)
	}
	if testUI["manifest.json"].Data[9] != 'T' {
		t.Error("不该改动原来的清单")
	}
	if w := get("POST", "/"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 页面：%d", w.Code)
	}

	// 没构建前端时给出提示。
	w = serve(spa(fstest.MapFS{}, site), http.MethodGet, "/")
	if !strings.Contains(w.Body.String(), "pnpm build") {
		t.Errorf("没构建时的提示：%s", w.Body)
	}
}

func TestHome(t *testing.T) {
	site := &fakeSite{site: settings.Site{Name: "Tillbox"}}
	h := spa(testUI, site)
	for _, p := range []string{"/", "/index.html"} {
		site.home = settings.Home{Mode: settings.HomePage, HTML: "<h1>我的小站</h1>", URL: "https://example.com"}
		w := serve(h, "GET", p)
		if w.Code != 200 || w.Body.String() != "<h1>我的小站</h1>" || !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox ") ||
			strings.Contains(w.Header().Get("Content-Security-Policy"), "allow-same-origin") || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Errorf("%s 自定义页面：%d %v %s", p, w.Code, w.Header(), w.Body)
		}
		if w := serve(h, "HEAD", p); w.Code != 200 || w.Body.Len() != 0 {
			t.Errorf("%s HEAD：%d %s", p, w.Code, w.Body)
		}

		site.home.Mode = settings.HomeRedirect
		if w := serve(h, "GET", p); w.Code != http.StatusFound || w.Header().Get("Location") != "https://example.com" {
			t.Errorf("%s 跳转：%d %v", p, w.Code, w.Header())
		}

		site.home.Mode = settings.HomeNotFound
		if w := serve(h, "GET", p); w.Code != 404 || strings.Contains(w.Body.String(), "<title>") {
			t.Errorf("%s 404：%d %s", p, w.Code, w.Body)
		}

		site.home.Mode = settings.HomeHelp
		if w := serve(h, "GET", p); w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Tillbox</title>") || w.Header().Get("Content-Security-Policy") != "" {
			t.Errorf("%s 付款人自助：%d %s", p, w.Code, w.Body)
		}
	}
	// 首页怎么设都不影响收银台和付款人自助
	site.home = settings.Home{Mode: settings.HomeNotFound}
	for _, p := range []string{"/help", "/cashier/1"} {
		if w := serve(h, "GET", p); w.Code != 200 {
			t.Errorf("%s：%d", p, w.Code)
		}
	}
}

func TestEntryGate(t *testing.T) {
	site := &fakeSite{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("next")) })
	h := entryGate(next, site, func(*http.Request) bool { return true })
	passes := func(method, target string, cookie *http.Cookie) bool {
		r := httptest.NewRequest(method, target, nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code == 200 && w.Body.String() == "next"
	}
	backend := []string{"/admin", "/admin/", "/admin/orders", "//admin", "/login", "/web-api/admin/orders", "/web-api/auth/me"}
	public := []string{"/", "/help", "/cashier/1", "/web-api/cashier/1", "/web-api/payment-test/notify", "/submit.php", "/ADMIN"}

	// 没开时都放行
	for _, p := range append(backend, public...) {
		if !passes("GET", p, nil) {
			t.Errorf("没开安全入口时 %s 应放行", p)
		}
	}

	site.entry = settings.Entry{Key: "k3y"}
	for _, p := range public {
		if !passes("GET", p, nil) {
			t.Errorf("%s 不是后台，应放行", p)
		}
	}
	for _, p := range append(backend, "/admin?entry=wrong", "/login?entry=") {
		w := serve(h, "GET", p)
		want := "404 page not found\n"
		if strings.HasPrefix(p, "/web-api/") {
			want = "{\"error\":\"接口不存在\"}\n"
		}
		if w.Code != 404 || w.Body.String() != want {
			t.Errorf("%s 没入口 cookie 应和不存在的路径一样：%d %q", p, w.Code, w.Body)
		}
	}
	if passes("POST", "/web-api/auth/login", nil) {
		t.Error("没入口 cookie 不能试密码")
	}
	// 入口链接只认后台和登录页，用了以后记 cookie、跳回去掉密钥的地址
	if w := serve(h, "GET", "/help?entry=k3y"); w.Code != 200 || len(w.Result().Cookies()) != 0 {
		t.Errorf("别的页面带密钥不该放行：%d %v", w.Code, w.Result().Cookies())
	}
	w := serve(h, "GET", "/admin?entry=k3y&redirect=%2Fadmin%2Forders")
	cookies := w.Result().Cookies()
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin?redirect=%2Fadmin%2Forders" || len(cookies) != 1 {
		t.Fatalf("入口链接：%d %v", w.Code, w.Header())
	}
	c := cookies[0]
	if c.Name != admin.EntryCookie || c.Value != admin.EntryToken("k3y") || strings.Contains(c.Value, "k3y") ||
		!c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge < 300*24*3600 {
		t.Errorf("入口 cookie：%+v", c)
	}
	if w := serve(h, "GET", "/login?entry=k3y"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Errorf("登录页的入口链接：%d %v", w.Code, w.Header())
	}
	for _, p := range append(backend, "/admin?entry=old") {
		if !passes("GET", p, &http.Cookie{Name: admin.EntryCookie, Value: c.Value}) {
			t.Errorf("有入口 cookie 时 %s 应放行", p)
		}
	}
	// 换了密钥，旧 cookie 失效
	site.entry.Key = "n3w"
	if passes("GET", "/admin", &http.Cookie{Name: admin.EntryCookie, Value: c.Value}) {
		t.Error("换密钥后旧 cookie 不该再放行")
	}
}
