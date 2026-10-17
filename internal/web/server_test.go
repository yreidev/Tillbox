package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
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

func TestSPA(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":        {Data: []byte("<!doctype html><title>Tillbox</title>")},
		"assets/app-1.js":   {Data: []byte("console.log(1)")},
		"favicon.svg":       {Data: []byte("<svg/>")},
		"assets/zxing.wasm": {Data: []byte{0, 'a', 's', 'm'}},
	}
	h := spa(ui)
	get := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	if w := get("GET", "/assets/app-1.js"); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("静态资源：%d %v", w.Code, w.Header())
	}
	if w := get("GET", "/assets/zxing.wasm"); w.Code != 200 || w.Header().Get("Content-Type") != "application/wasm" {
		t.Errorf("wasm 的类型：%v", w.Header())
	}
	if w := get("GET", "/favicon.svg"); w.Code != 200 || w.Header().Get("Cache-Control") != "" {
		t.Errorf("根目录文件不该长缓存：%v", w.Header())
	}
	for _, p := range []string{"/assets/missing.js", "/missing.png"} {
		if w := get("GET", p); w.Code != 404 {
			t.Errorf("%s 应 404：%d", p, w.Code)
		}
	}
	for _, p := range []string{"/", "/index.html", "/cashier/2026101500000000001", "/admin/orders", "/login"} {
		w := get("GET", p)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Tillbox</title>") || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s 应回退到 index.html：%d %s", p, w.Code, w.Body)
		}
		framed := w.Header().Get("X-Frame-Options") == "DENY"
		if want := strings.HasPrefix(p, "/admin") || p == "/login"; framed != want {
			t.Errorf("%s 的 X-Frame-Options：%v", p, w.Header())
		}
	}
	if w := get("POST", "/"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 页面：%d", w.Code)
	}
	if w := get("GET", "/../../etc/passwd"); w.Code != 200 || !strings.Contains(w.Body.String(), "Tillbox") {
		t.Errorf("路径穿越：%d %s", w.Code, w.Body)
	}

	// 没构建前端时给出提示。
	w := httptest.NewRecorder()
	spa(fstest.MapFS{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(w.Body.String(), "pnpm build") {
		t.Errorf("没构建时的提示：%s", w.Body)
	}
}
