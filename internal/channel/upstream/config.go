package upstream

import (
	"encoding/json"
	"net"
	"net/url"
	"slices"
	"strings"

	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/secret"
)

// Config 是第三方易支付渠道的配置，存在渠道表的 config 列。
type Config struct {
	APIURL            string   `json:"api_url"` // 第三方平台接口地址，不带末尾的 /
	Version           int      `json:"version"` // 1：MD5；2：RSA
	PID               string   `json:"pid"`
	Key               string   `json:"key"`         // V1 商户密钥，主密钥加密
	PrivateKey        string   `json:"private_key"` // V2 商户私钥，主密钥加密
	PublicKey         string   `json:"public_key"`  // V2 商户公钥，由私钥算出，交给第三方平台
	PlatformPublicKey string   `json:"platform_public_key"`
	Types             []string `json:"types"` // 开放的支付方式
	// Limits 是各支付方式的单笔限额，见 limit.go。
	Limits map[string]Limit `json:"limits,omitempty"`
}

// ParseConfig 解析渠道表里的配置，没填版本时按 V1。
func ParseConfig(raw string) (Config, error) {
	c := Config{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return c, channel.Invalid("第三方易支付渠道配置格式不对：%v", err)
		}
	}
	if c.Version == 0 {
		c.Version = 1
	}
	return c, nil
}

// Input 是后台提交的第三方易支付渠道配置。密钥留空表示保留原值。
type Input struct {
	APIURL            string   `json:"api_url"`
	Version           int      `json:"version"`
	PID               string   `json:"pid"`
	Key               string   `json:"key"`
	PrivateKey        string   `json:"private_key"`
	PlatformPublicKey string   `json:"platform_public_key"`
	Types             []string `json:"types"`
	// Limits 为空时保留原来的限额。
	Limits map[string]LimitInput `json:"limits"`
}

// View 是给后台看的配置，不含密钥。
type View struct {
	APIURL            string               `json:"api_url"`
	Version           int                  `json:"version"`
	PID               string               `json:"pid"`
	HasKey            bool                 `json:"has_key"`
	HasPrivateKey     bool                 `json:"has_private_key"`
	PublicKey         string               `json:"public_key"`
	PlatformPublicKey string               `json:"platform_public_key"`
	Types             []string             `json:"types"`
	Limits            map[string]LimitView `json:"limits"`
}

// ViewOf 把渠道表里的配置转成后台看的样子。
func ViewOf(raw string) View {
	c, _ := ParseConfig(raw)
	types := c.Types
	if types == nil {
		types = []string{}
	}
	return View{
		APIURL: c.APIURL, Version: c.Version, PID: c.PID, HasKey: c.Key != "", HasPrivateKey: c.PrivateKey != "",
		PublicKey: c.PublicKey, PlatformPublicKey: c.PlatformPublicKey, Types: types, Limits: limitViews(c),
	}
}

// localHost 判断是不是本机地址（本机允许 HTTP，方便本地联调）。
func localHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// normalizeURL 校验第三方平台接口地址：HTTPS（本机地址可以 HTTP），不带查询串和片段，去掉末尾的 /。
func normalizeURL(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	for _, suffix := range []string{"/submit.php", "/mapi.php", "/api.php"} {
		s = strings.TrimSuffix(s, suffix)
	}
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(s) > 500 {
		return "", channel.Invalid("第三方平台接口地址不对，应该是第三方易支付的根地址，例如 https://pay.example.com/")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && localHost(u.Hostname())) {
		return "", channel.Invalid("第三方平台接口地址必须是 https:// 开头（本机地址除外）")
	}
	return s, nil
}

func validPID(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	return strings.Trim(s, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_-") == ""
}

// Merge 校验后台提交的配置，合并进当前配置后返回新的配置原文。密钥加密保存，没填时保留原值。
func Merge(box *secret.Box, cur string, in json.RawMessage, enabled bool) (string, error) {
	c, err := ParseConfig(cur)
	if err != nil {
		c = Config{Version: 1}
	}
	var v Input
	if len(in) > 0 {
		if err := json.Unmarshal(in, &v); err != nil {
			return "", channel.Invalid("第三方易支付配置格式不对")
		}
	}
	api, err := normalizeURL(v.APIURL)
	if err != nil {
		return "", err
	}
	if v.Version != 1 && v.Version != 2 {
		return "", channel.Invalid("接口版本只能选 V1（MD5）或 V2（RSA）")
	}
	pid := strings.TrimSpace(v.PID)
	if pid != "" && !validPID(pid) {
		return "", channel.Invalid("商户号只能是字母、数字、下划线和短横线，不超过 32 个字符")
	}
	key := strings.TrimSpace(v.Key)
	if key != "" && (len(key) > 128 || strings.ContainsAny(key, " \t\r\n")) {
		return "", channel.Invalid("商户密钥不超过 128 个字符，不能有空格")
	}
	// 换了第三方平台地址就要重新填商户密钥：V1 查单会把密钥明文发给第三方平台。
	if c.Key != "" && key == "" && api != c.APIURL {
		return "", channel.Invalid("换了第三方平台接口地址，要重新填写商户密钥")
	}
	platform := strings.Join(strings.Fields(v.PlatformPublicKey), "")
	if platform != "" {
		if _, err := epay.ParsePublicKey(platform); err != nil {
			return "", channel.Invalid("平台公钥格式不对：请粘贴第三方平台给的「平台公钥」")
		}
	}
	types := make([]string, 0, len(v.Types))
	for _, t := range v.Types {
		t = strings.TrimSpace(t)
		if channel.TypeNames[t] == "" {
			return "", channel.Invalid("不认识的支付方式 %q", t)
		}
		if !slices.Contains(types, t) {
			types = append(types, t)
		}
	}
	limits, err := mergeLimits(c.Limits, v.Limits, types)
	if err != nil {
		return "", err
	}
	c.APIURL, c.Version, c.PID, c.PlatformPublicKey, c.Types, c.Limits = api, v.Version, pid, platform, types, limits
	if key != "" {
		sealed, err := box.Seal(key)
		if err != nil {
			return "", err
		}
		c.Key = sealed
	}
	if k := strings.TrimSpace(v.PrivateKey); k != "" {
		priv, err := epay.ParsePrivateKey(k)
		if err != nil {
			return "", channel.Invalid("商户私钥格式不对：%v", err)
		}
		if c.PublicKey, err = epay.PublicKeyOf(priv); err != nil {
			return "", err
		}
		plain, err := epay.MarshalPrivateKey(priv)
		if err != nil {
			return "", err
		}
		if c.PrivateKey, err = box.Seal(plain); err != nil {
			return "", err
		}
	}
	if enabled {
		if err := complete(c); err != nil {
			return "", channel.Invalid("启用前要填完整：%v", err)
		}
	}
	b, err := json.Marshal(c)
	return string(b), err
}

// complete 检查配置是否齐全，缺什么就说什么。
func complete(c Config) error {
	switch {
	case c.APIURL == "" || c.PID == "":
		return errText("第三方平台接口地址和商户号没有填")
	case c.Version == 1 && c.Key == "":
		return errText("V1 要填商户密钥")
	case c.Version == 2 && (c.PrivateKey == "" || c.PlatformPublicKey == ""):
		return errText("V2 要有商户私钥和平台公钥")
	case len(c.Types) == 0:
		return errText("至少勾选一种支付方式")
	}
	return nil
}

type errText string

func (e errText) Error() string { return string(e) }

// Keypair 生成新的商户 RSA 密钥对（V2 用）：私钥加密写进配置，公钥返回给用户填到第三方平台商户后台。
func Keypair(box *secret.Box, cur string) (cfg, pub string, err error) {
	c, err := ParseConfig(cur)
	if err != nil {
		c = Config{Version: 2}
	}
	priv, pub, err := epay.GenerateKeyPair()
	if err != nil {
		return "", "", err
	}
	sealed, err := box.Seal(priv)
	if err != nil {
		return "", "", err
	}
	c.PrivateKey, c.PublicKey = sealed, pub
	b, err := json.Marshal(c)
	return string(b), pub, err
}
