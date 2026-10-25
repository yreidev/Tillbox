// Package update 定时向 GitHub 查 Tillbox 有没有新的正式版，在后台提示管理员。
// 只读公开的标签列表，不带任何本站信息；可以在“设置 → 站点”里关掉。
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"tillbox/internal/settings"
)

const (
	// TagsURL 是 Tillbox 仓库的标签列表。仓库只打标签、不建 Release，所以查标签。
	TagsURL = "https://api.github.com/repos/yreidev/Tillbox/tags?per_page=100"
	// NotesURL 是某个版本的更新说明，%s 是 v 开头的标签。
	NotesURL = "https://github.com/yreidev/Tillbox/blob/%s/CHANGELOG.md"
	// Interval 是多久查一次。GitHub 未登录的接口每个 IP 每小时限 60 次，这里一天两次。
	Interval = 12 * time.Hour
	stateKey = "update_state"
)

// State 是上次检查的结果，存在设置表里。
type State struct {
	CheckedAt int64  `json:"checked_at"`
	Latest    string `json:"latest"` // 最新正式版，不带 v，例如 0.9.0
	Error     string `json:"error"`
}

// View 是给后台看的版本信息。
type View struct {
	Current   string `json:"current"`    // 正在运行的版本，开发构建是 dev
	Latest    string `json:"latest"`     // 查到的最新正式版
	Newer     bool   `json:"newer"`      // 有比当前更新的正式版
	NotesURL  string `json:"notes_url"`  // 最新版的更新说明
	CheckedAt int64  `json:"checked_at"` // 上次检查的时刻
	Error     string `json:"error"`      // 上次检查失败的原因
	Check     bool   `json:"check"`      // 是否定时检查
	Revision  int64  `json:"revision"`   // 设置的版本，保存时放进 If-Match
}

type Service struct {
	Settings *settings.Service
	Current  string
	URL      string
	HTTP     *http.Client
	// Now 可以在测试里替换。
	Now func() time.Time

	mu sync.Mutex
}

func New(st *settings.Service, current string) *Service {
	return &Service{
		Settings: st, Current: current, URL: TagsURL, Now: time.Now,
		HTTP: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}},
	}
}

// Run 每小时看一次，开着检查且距上次超过 Interval 时向 GitHub 查一次，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if cfg, err := s.Settings.Update(ctx); err == nil && cfg.Check {
			if st, err := s.Status(ctx); err == nil && s.Now().Sub(time.UnixMilli(st.CheckedAt)) >= Interval {
				_, _ = s.Check(ctx)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) Status(ctx context.Context) (State, error) {
	var v State
	_, err := s.Settings.Raw(ctx, stateKey, &v)
	return v, err
}

// View 汇总当前版本、上次检查结果和设置。
func (s *Service) View(ctx context.Context) (View, error) {
	st, err := s.Status(ctx)
	if err != nil {
		return View{}, err
	}
	cfg, err := s.Settings.Update(ctx)
	if err != nil {
		return View{}, err
	}
	rev, err := s.Settings.Revision(ctx, settings.KeyUpdate)
	if err != nil {
		return View{}, err
	}
	v := View{Current: s.Current, Latest: st.Latest, CheckedAt: st.CheckedAt, Error: st.Error, Check: cfg.Check, Revision: rev}
	if st.Latest != "" {
		v.NotesURL = fmt.Sprintf(NotesURL, "v"+st.Latest)
		v.Newer = Newer(st.Latest, s.Current)
	}
	return v, nil
}

// Check 马上查一次，结果存起来。查不到时保留上次查到的版本。
func (s *Service) Check(ctx context.Context) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.Status(ctx)
	if err != nil {
		return st, err
	}
	latest, err := s.fetch(ctx)
	st.CheckedAt = s.Now().UnixMilli()
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Latest, st.Error = latest, ""
	}
	if saveErr := s.Settings.SaveRaw(ctx, stateKey, st); saveErr != nil && err == nil {
		err = saveErr
	}
	return st, err
}

func (s *Service) fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Tillbox")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", errors.New("连不上 GitHub，请检查服务器能否访问 api.github.com")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("读取 GitHub 响应失败：%w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub 返回 HTTP %d", resp.StatusCode)
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		return "", errors.New("GitHub 返回的格式不对")
	}
	latest := ""
	for _, t := range tags {
		// 只认 vX.Y.Z 的正式版，跳过预发布和别的标签。
		if !semver.IsValid(t.Name) || semver.Prerelease(t.Name) != "" || semver.Build(t.Name) != "" || semver.Canonical(t.Name) != t.Name {
			continue
		}
		if latest == "" || semver.Compare(t.Name, latest) > 0 {
			latest = t.Name
		}
	}
	if latest == "" {
		return "", errors.New("GitHub 上没有找到正式版的标签")
	}
	return strings.TrimPrefix(latest, "v"), nil
}

// Newer 判断 latest 是否比 current 新。current 不是正式版号（例如开发构建的 dev）时不提示。
func Newer(latest, current string) bool {
	l, c := "v"+latest, "v"+strings.TrimPrefix(current, "v")
	return semver.IsValid(l) && semver.IsValid(c) && semver.Compare(l, c) > 0
}
