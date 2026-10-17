package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"tillbox/internal/settings"
)

// 前端页面：首页按“设置 → 站点”显示；收银台、付款自助、后台和登录页回退到 index.html 由前端路由处理；
// 其余不是文件的路径一律 404，不让随便一个地址都返回 Tillbox 的页面。

type siteSettings interface {
	Site(ctx context.Context) (settings.Site, error)
	Home(ctx context.Context) (settings.Home, error)
}

const notBuilt = `<!doctype html><meta charset="utf-8"><title>Tillbox</title><p>前端还没有构建。先在 web/ 目录运行 pnpm install && pnpm build，再重新编译。</p>`

// homeCSP 让自定义首页里的脚本在隔离的来源里运行：读不到本站的 cookie 和本地存储，也不能带着登录状态调后台接口。
const homeCSP = "sandbox allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads"

var (
	titleTag   = regexp.MustCompile(`<title>[^<]*</title>`)
	appNameTag = regexp.MustCompile(`<meta name="application-name" content="[^"]*"`)
)

// route 判断路径（去掉开头的 /）是不是前端页面，和 web/src/routes 一致。
func route(p string) bool {
	return p == "help" || p == "login" || p == "admin" || strings.HasPrefix(p, "admin/") || strings.HasPrefix(p, "cashier/")
}

type pages struct {
	ui       fs.FS
	files    http.Handler
	index    []byte
	manifest map[string]any
	site     siteSettings
}

func spa(ui fs.FS, site siteSettings) http.Handler {
	p := &pages{ui: ui, files: http.FileServerFS(ui), site: site}
	p.index, _ = fs.ReadFile(ui, "index.html")
	if raw, err := fs.ReadFile(ui, "manifest.json"); err == nil {
		_ = json.Unmarshal(raw, &p.manifest)
	}
	return p
}

func (p *pages) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	switch {
	case name == "" || name == "index.html":
		p.home(w, r)
		return
	case name == "manifest.json" && p.manifest != nil:
		p.serveManifest(w, r)
		return
	}
	if st, err := fs.Stat(p.ui, name); err == nil && !st.IsDir() {
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		p.files.ServeHTTP(w, r)
		return
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !route(name) {
		http.NotFound(w, r)
		return
	}
	p.app(w, r, name)
}

// home 是直接打开域名时的页面。
func (p *pages) home(w http.ResponseWriter, r *http.Request) {
	h, err := p.site.Home(r.Context())
	if err != nil {
		slog.Error("读取首页设置失败", "err", err)
		http.Error(w, "服务器出错", http.StatusInternalServerError)
		return
	}
	switch h.Mode {
	case settings.HomePage:
		hd := w.Header()
		hd.Set("Content-Type", "text/html; charset=utf-8")
		hd.Set("Cache-Control", "no-cache")
		hd.Set("Content-Security-Policy", homeCSP)
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader(h.HTML))
	case settings.HomeRedirect:
		// 302：浏览器不缓存，改回别的首页立即生效
		http.Redirect(w, r, h.URL, http.StatusFound)
	case settings.HomeNotFound:
		http.NotFound(w, r)
	default:
		p.app(w, r, "")
	}
}

// app 返回前端的 index.html，标题和应用名换成站点名。
func (p *pages) app(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if name == "admin" || strings.HasPrefix(name, "admin/") || name == "login" {
		w.Header().Set("X-Frame-Options", "DENY")
	}
	if p.index == nil {
		_, _ = w.Write([]byte(notBuilt))
		return
	}
	page := p.index
	if site, err := p.site.Site(r.Context()); err == nil && site.Name != "" {
		n := html.EscapeString(site.Name)
		page = titleTag.ReplaceAllLiteral(page, []byte("<title>"+n+"</title>"))
		page = appNameTag.ReplaceAllLiteral(page, []byte(`<meta name="application-name" content="`+n+`"`))
	}
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(page))
}

// serveManifest 返回 PWA 清单，名字换成站点名（添加到桌面时显示的名字）。
func (p *pages) serveManifest(w http.ResponseWriter, r *http.Request) {
	m := maps.Clone(p.manifest)
	if site, err := p.site.Site(r.Context()); err == nil && site.Name != "" {
		m["name"], m["short_name"] = site.Name, site.Name
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(m)
}
