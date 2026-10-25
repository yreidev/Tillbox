package update_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"tillbox/internal/apptest"
	"tillbox/internal/update"
)

func TestCheck(t *testing.T) {
	e := apptest.New(t)
	body := `[{"name":"v0.10.0-rc.1"},{"name":"v0.9.0"},{"name":"v0.10.0"},{"name":"v0.8.0"},{"name":"latest"},{"name":"v1.0"}]`
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("请求 GitHub 要带 User-Agent")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	s := update.New(e.App.Settings, "0.8.0")
	s.URL = srv.URL

	v, err := s.View(e.Ctx)
	if err != nil || v.Latest != "" || v.Newer || !v.Check {
		t.Fatalf("还没查过时：%+v %v", v, err)
	}
	if _, err := s.Check(e.Ctx); err != nil {
		t.Fatal(err)
	}
	v, _ = s.View(e.Ctx)
	if v.Latest != "0.10.0" || !v.Newer || v.NotesURL != "https://github.com/yreidev/Tillbox/blob/v0.10.0/CHANGELOG.md" || v.CheckedAt == 0 {
		t.Fatalf("应找到最新正式版 0.10.0，跳过预发布和别的标签：%+v", v)
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
