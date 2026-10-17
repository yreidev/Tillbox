package settings

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

// 首页（直接打开域名时）显示什么。
const (
	HomeHelp     = "help"      // 付款人自助页（默认）
	HomePage     = "page"      // 管理员贴的一整段 HTML
	HomeRedirect = "redirect"  // 跳转到指定网址
	HomeNotFound = "not_found" // 404
)

// MaxHomeHTML 是自定义首页 HTML 的字节上限。
const MaxHomeHTML = 512 << 10

// Home 是首页设置。切换方式时另外两项保留，换回来不用重填。
type Home struct {
	Mode string `json:"mode"`
	HTML string `json:"html"`
	URL  string `json:"url"`
}

func (h Home) Validate() error {
	if len(h.HTML) > MaxHomeHTML {
		return errors.New("自定义页面不能超过 512 KB")
	}
	if len(h.URL) > 2048 {
		return errors.New("跳转网址太长")
	}
	switch h.Mode {
	case HomeHelp, HomeNotFound:
	case HomePage:
		if strings.TrimSpace(h.HTML) == "" {
			return errors.New("请填写自定义页面的 HTML")
		}
	case HomeRedirect:
		u, err := url.Parse(h.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("跳转网址要是 http:// 或 https:// 开头的完整网址")
		}
	default:
		return errors.New("不认识的首页类型")
	}
	return nil
}

// Entry 是后台安全入口。Key 为空表示没开：开着时要先打开带 Key 的入口链接，才看得到后台和登录页。
type Entry struct {
	Key string `json:"key"`
}

const (
	keyHome  = "home"
	keyEntry = "entry"
)

func (s *Service) Home(ctx context.Context) (Home, error) {
	h := Home{Mode: HomeHelp}
	if _, err := s.load(ctx, keyHome, &h); err != nil {
		return h, err
	}
	return h, nil
}

func (s *Service) SaveHome(ctx context.Context, h Home) error {
	if err := h.Validate(); err != nil {
		return err
	}
	return s.save(ctx, keyHome, h)
}

// Entry 每次都从库里读：命令行 tillbox entry 改了以后，运行中的服务马上生效。
func (s *Service) Entry(ctx context.Context) (Entry, error) {
	s.mu.Lock()
	delete(s.cache, keyEntry)
	s.mu.Unlock()
	var e Entry
	_, err := s.load(ctx, keyEntry, &e)
	return e, err
}

// ResetEntry 开启安全入口或换一个新密钥，旧的入口链接立即失效。
func (s *Service) ResetEntry(ctx context.Context) (Entry, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return Entry{}, err
	}
	e := Entry{Key: base64.RawURLEncoding.EncodeToString(b)}
	return e, s.save(ctx, keyEntry, e)
}

// DisableEntry 关闭安全入口。
func (s *Service) DisableEntry(ctx context.Context) error {
	return s.save(ctx, keyEntry, Entry{})
}
