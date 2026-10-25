package update_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"tillbox/internal/apptest"
	"tillbox/internal/update"
)

const changelog = `# Changelog

## 未发布

- 还没发布的改动

## 0.10.0 — 2026-11-02

- 新功能，见 ` + "`docs/x.md`" + `
- 很长的一条
  接着上一行写

## 0.9.0 — 2026-10-30

- 修了一个问题

## 0.8.0 — 2026-10-20

- 已经装上的版本
`

func TestCheck(t *testing.T) {
	e := apptest.New(t)
	tags := `[{"name":"v0.10.0-rc.1"},{"name":"v0.9.0"},{"name":"v0.10.0"},{"name":"v0.8.0"},{"name":"latest"},{"name":"v1.0"}]`
	migrations := `[{"name":"00001_init.sql"},{"name":"00007_new.sql"},{"name":"00008_more.sql"}]`
	status, detail := http.StatusOK, http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("请求 GitHub 要带 User-Agent")
		}
		switch {
		case r.URL.Path == "/tags":
			w.WriteHeader(status)
			_, _ = w.Write([]byte(tags))
		case r.URL.Path == "/raw/v0.10.0/CHANGELOG.md":
			w.WriteHeader(detail)
			_, _ = w.Write([]byte(changelog))
		case r.URL.Path == "/contents" && r.URL.Query().Get("ref") == "v0.10.0":
			w.WriteHeader(detail)
			_, _ = w.Write([]byte(migrations))
		default:
			t.Errorf("不该请求 %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	s := update.New(e.App.Settings, "0.8.0")
	s.URL, s.ChangelogURL, s.MigrationsURL = srv.URL+"/tags", srv.URL+"/raw/%s/CHANGELOG.md", srv.URL+"/contents?ref=%s"
	s.Schema = 6

	v, err := s.View(e.Ctx)
	if err != nil || v.Latest != "" || v.Newer || !v.Check || v.Image != update.Image {
		t.Fatalf("还没查过时：%+v %v", v, err)
	}
	if _, err := s.Check(e.Ctx); err != nil {
		t.Fatal(err)
	}
	v, _ = s.View(e.Ctx)
	if v.Latest != "0.10.0" || !v.Newer || v.NotesURL != "https://github.com/yreidev/Tillbox/blob/v0.10.0/CHANGELOG.md" || v.CheckedAt == 0 {
		t.Fatalf("应找到最新正式版 0.10.0，跳过预发布和别的标签：%+v", v)
	}
	if len(v.Releases) != 2 || v.Releases[0].Version != "0.10.0" || v.Releases[1].Version != "0.9.0" {
		t.Fatalf("应列出 0.10.0 和 0.9.0 的更新内容，不含未发布和已装上的版本：%+v", v.Releases)
	}
	if v.Migrations != 2 {
		t.Errorf("从迁移 6 升到 8 要执行 2 项迁移，实际 %d", v.Migrations)
	}

	// 升级到 0.9.0 以后再看：0.9.0 不再列出，迁移按新版本带的编号算。
	s.Current, s.Schema = "0.9.0", 7
	v, _ = s.View(e.Ctx)
	if len(v.Releases) != 1 || v.Releases[0].Version != "0.10.0" || v.Migrations != 1 {
		t.Errorf("升级到 0.9.0 后：%+v", v)
	}
	s.Current, s.Schema = "0.8.0", 6

	// 更新日志和迁移列表取不到不算检查失败；同一个最新版之前取到的内容保留。
	detail = http.StatusInternalServerError
	if _, err := s.Check(e.Ctx); err != nil {
		t.Fatalf("取不到更新日志不该算检查失败：%v", err)
	}
	if v, _ = s.View(e.Ctx); len(v.Releases) != 2 || v.Migrations != 2 || v.Error != "" {
		t.Errorf("应保留之前取到的更新内容：%+v", v)
	}

	// 查失败时保留上次查到的版本，记下原因。
	status = http.StatusForbidden
	if _, err := s.Check(e.Ctx); err == nil {
		t.Fatal("GitHub 返回错误时应报错")
	}
	v, _ = s.View(e.Ctx)
	if v.Latest != "0.10.0" || v.Error == "" {
		t.Errorf("失败时应保留上次的结果并记下原因：%+v", v)
	}

	// 已经是最新版时不带升级指引。
	s.Current = "0.10.0"
	status, detail = http.StatusOK, http.StatusOK
	if _, err := s.Check(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ = s.View(e.Ctx); v.Newer || len(v.Releases) != 0 || v.Migrations != 0 {
		t.Errorf("已是最新版：%+v", v)
	}
}

// 新版本的迁移列表取不到时，升级指引显示“没查到”（-1），按有数据库变更对待。
func TestMigrationsUnknown(t *testing.T) {
	e := apptest.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tags" {
			_, _ = w.Write([]byte(`[{"name":"v0.9.0"}]`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	s := update.New(e.App.Settings, "0.8.0")
	s.URL, s.ChangelogURL, s.MigrationsURL = srv.URL+"/tags", srv.URL+"/raw/%s", srv.URL+"/contents/%s"
	if _, err := s.Check(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.View(e.Ctx); !v.Newer || v.Migrations != -1 || len(v.Releases) != 0 {
		t.Errorf("取不到详情时：%+v", v)
	}
}

func TestParseChangelog(t *testing.T) {
	rs := update.ParseChangelog(changelog)
	if len(rs) != 3 || rs[0].Version != "0.10.0" || rs[0].Date != "2026-11-02" || rs[2].Version != "0.8.0" {
		t.Fatalf("应读出三个版本：%+v", rs)
	}
	if len(rs[0].Items) != 2 || rs[0].Items[0] != "新功能，见 `docs/x.md`" || rs[0].Items[1] != "很长的一条 接着上一行写" {
		t.Errorf("列表项和续行：%q", rs[0].Items)
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"0.9.0", "0.8.0", true},
		{"0.10.0", "0.9.0", true},
		{"0.8.0", "0.8.0", false},
		{"0.8.0", "0.9.0", false},
		{"0.9.0", "v0.8.0", true},
		{"0.9.0", "dev", false},
		{"", "0.8.0", false},
	} {
		if got := update.Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v，应为 %v", c.latest, c.current, got, c.want)
		}
	}
}
