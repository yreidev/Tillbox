// Package update 定时向 GitHub 查 Tillbox 有没有新的正式版，在后台提示管理员，并给出升级指引。
// 只读公开的标签列表、更新日志和迁移文件列表，不带任何本站信息；可以在“设置 → 站点”里关掉。
// 升级本身由管理员在服务器上操作，Tillbox 不会自己升级。
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"tillbox/internal/settings"
	"tillbox/internal/store"
)

const (
	// TagsURL 是 Tillbox 仓库的标签列表。仓库只打标签、不建 Release，所以查标签。
	TagsURL = "https://api.github.com/repos/yreidev/Tillbox/tags?per_page=100"
	// NotesURL 是某个版本的更新说明，%s 是 v 开头的标签。
	NotesURL = "https://github.com/yreidev/Tillbox/blob/%s/CHANGELOG.md"
	// ChangelogURL 是某个版本的更新日志原文，%s 是 v 开头的标签。
	ChangelogURL = "https://raw.githubusercontent.com/yreidev/Tillbox/%s/CHANGELOG.md"
	// MigrationsURL 列出某个版本带的数据库迁移文件，%s 是 v 开头的标签。两种数据库的迁移编号一致，看 SQLite 的就够。
	MigrationsURL = "https://api.github.com/repos/yreidev/Tillbox/contents/internal/store/migrations/sqlite?ref=%s"
	// Image 是发布的镜像，标签是不带 v 的版本号。
	Image = "ghcr.io/yreidev/tillbox"
	// Interval 是多久查一次。GitHub 未登录的接口每个 IP 每小时限 60 次，这里一天两次，每次最多调两个接口。
	Interval = 12 * time.Hour
	// maxReleases 是升级指引最多列几个版本的更新内容，落后太多时其余的看更新说明链接。
	maxReleases = 20
	stateKey    = "update_state"
)

// State 是上次检查的结果，存在设置表里。
type State struct {
	CheckedAt int64  `json:"checked_at"`
	Latest    string `json:"latest"` // 最新正式版，不带 v，例如 0.9.0
	Error     string `json:"error"`
	// Releases 是最新版的更新日志里比检查时正在运行的版本新的各版，新的在前；没取到时为空。
	Releases []Release `json:"releases,omitempty"`
	// Schema 是最新版带的数据库迁移编号，0 是没取到。
	Schema int `json:"schema,omitempty"`
}

// Release 是更新日志里一个版本的内容。
type Release struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Items   []string `json:"items"` // 每条是更新日志的一个列表项，原样保留反引号
}

// Deploy 是检测到的部署方式，升级指引按它先给对应的命令。
type Deploy struct {
	Mode      string `json:"mode"`      // kubernetes、docker 或 binary
	Namespace string `json:"namespace"` // Kubernetes 命名空间，读不到时为空
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
	// 下面是升级指引，只在 Newer 时有意义。
	Releases   []Release `json:"releases"`   // 比当前新的各版更新内容，新的在前
	Migrations int       `json:"migrations"` // 升级到最新版要执行几项数据库迁移，-1 是没查到
	Deploy     Deploy    `json:"deploy"`
	Image      string    `json:"image"`
}

type Service struct {
	Settings *settings.Service
	Current  string
	// Schema 是正在运行的版本带的数据库迁移编号。
	Schema        int
	Deploy        Deploy
	URL           string
	ChangelogURL  string
	MigrationsURL string
	HTTP          *http.Client
	// Now 可以在测试里替换。
	Now func() time.Time

	mu sync.Mutex
}

func New(st *settings.Service, current string) *Service {
	return &Service{
		Settings: st, Current: current, Schema: store.SchemaVersion(), Deploy: DetectDeploy(),
		URL: TagsURL, ChangelogURL: ChangelogURL, MigrationsURL: MigrationsURL, Now: time.Now,
		HTTP: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}},
	}
}

// DetectDeploy 看运行环境：Kubernetes 会注入 KUBERNETES_SERVICE_HOST，Docker 和 Podman 在根目录留标记文件，
// 都不是就当二进制部署。命名空间从服务账号目录读，Deployment 关了 automountServiceAccountToken 时读不到。
func DetectDeploy() Deploy {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		ns, _ := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
		return Deploy{Mode: "kubernetes", Namespace: strings.TrimSpace(string(ns))}
	}
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return Deploy{Mode: "docker"}
		}
	}
	return Deploy{Mode: "binary"}
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

// View 汇总当前版本、上次检查结果、设置和升级指引。
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
	v := View{
		Current: s.Current, Latest: st.Latest, CheckedAt: st.CheckedAt, Error: st.Error, Check: cfg.Check, Revision: rev,
		Releases: []Release{}, Deploy: s.Deploy, Image: Image,
	}
	if st.Latest != "" {
		v.NotesURL = fmt.Sprintf(NotesURL, "v"+st.Latest)
		v.Newer = Newer(st.Latest, s.Current)
	}
	if v.Newer {
		// 检查之后升级过一次（还不是最新）时，已经装上的版本不再列出。
		for _, r := range st.Releases {
			if Newer(r.Version, s.Current) {
				v.Releases = append(v.Releases, r)
			}
		}
		v.Migrations = -1
		if st.Schema > 0 {
			v.Migrations = max(st.Schema-s.Schema, 0)
		}
	}
	return v, nil
}

// Check 马上查一次，结果存起来。查不到时保留上次查到的版本。有新版本时顺带取更新日志和迁移文件列表，
// 这两样取不到不算检查失败，升级指引里显示“没查到”。
func (s *Service) Check(ctx context.Context) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.Status(ctx)
	if err != nil {
		return st, err
	}
	latest, err := s.latest(ctx)
	st.CheckedAt = s.Now().UnixMilli()
	if err != nil {
		st.Error = err.Error()
	} else {
		if latest != st.Latest {
			st.Releases, st.Schema = nil, 0
		}
		st.Latest, st.Error = latest, ""
		if Newer(latest, s.Current) {
			if rs, err := s.releases(ctx, latest); err == nil {
				st.Releases = rs
			}
			if n, err := s.schema(ctx, latest); err == nil {
				st.Schema = n
			}
		} else {
			st.Releases, st.Schema = nil, 0
		}
	}
	if saveErr := s.Settings.SaveRaw(ctx, stateKey, st); saveErr != nil && err == nil {
		err = saveErr
	}
	return st, err
}

func (s *Service) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Tillbox")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("连不上 GitHub，请检查服务器能否访问 api.github.com 和 raw.githubusercontent.com")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 GitHub 响应失败：%w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub 返回 HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func (s *Service) latest(ctx context.Context) (string, error) {
	body, err := s.get(ctx, s.URL)
	if err != nil {
		return "", err
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

// releases 取最新版的更新日志，留下比正在运行的版本新、又不比最新版新的几版。
func (s *Service) releases(ctx context.Context, latest string) ([]Release, error) {
	body, err := s.get(ctx, fmt.Sprintf(s.ChangelogURL, "v"+latest))
	if err != nil {
		return nil, err
	}
	var out []Release
	for _, r := range ParseChangelog(string(body)) {
		if Newer(r.Version, s.Current) && !Newer(r.Version, latest) && len(out) < maxReleases {
			out = append(out, r)
		}
	}
	return out, nil
}

// schema 取最新版带的迁移文件，返回最大的迁移编号。
func (s *Service) schema(ctx context.Context, latest string) (int, error) {
	body, err := s.get(ctx, fmt.Sprintf(s.MigrationsURL, "v"+latest))
	if err != nil {
		return 0, err
	}
	var files []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &files); err != nil {
		return 0, errors.New("GitHub 返回的格式不对")
	}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	n := store.MaxMigration(names)
	if n == 0 {
		return 0, errors.New("没有找到迁移文件")
	}
	return n, nil
}

var releaseHeading = regexp.MustCompile(`^## (\d+\.\d+\.\d+)(?:\s+\S+\s+(\d{4}-\d{2}-\d{2}))?`)

// ParseChangelog 读 CHANGELOG.md：“## x.y.z — 日期”开始一个版本，下面“- ”开头的行是一条，缩进的续行接到上一条。
// “未发布”和别的标题跳过。
func ParseChangelog(text string) []Release {
	var out []Release
	cur := -1
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "#") {
			cur = -1
			if m := releaseHeading.FindStringSubmatch(line); m != nil {
				out = append(out, Release{Version: m[1], Date: m[2], Items: []string{}})
				cur = len(out) - 1
			}
			continue
		}
		if cur < 0 {
			continue
		}
		r := &out[cur]
		switch t := strings.TrimSpace(line); {
		case strings.HasPrefix(line, "- "):
			r.Items = append(r.Items, t[2:])
		case t != "" && strings.HasPrefix(line, " ") && len(r.Items) > 0:
			r.Items[len(r.Items)-1] += " " + t
		}
	}
	return out
}

// Newer 判断 latest 是否比 current 新。current 不是正式版号（例如开发构建的 dev）时不提示。
func Newer(latest, current string) bool {
	l, c := "v"+latest, "v"+strings.TrimPrefix(current, "v")
	return semver.IsValid(l) && semver.IsValid(c) && semver.Compare(l, c) > 0
}
