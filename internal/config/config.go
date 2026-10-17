// Package config 读取启动参数。只放启动时必须知道的东西，业务设置存库、在后台改。
package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	// 监听地址
	Listen string `env:"TILLBOX_LISTEN" envDefault:":8080"`
	// 数据目录：SQLite 数据库和自动生成的主密钥放在这里
	DataDir string `env:"TILLBOX_DATA_DIR" envDefault:"data"`
	// 数据库：sqlite 或 postgres
	DBDriver string `env:"TILLBOX_DB_DRIVER" envDefault:"sqlite"`
	// 连接串。sqlite 时为空就用 DataDir/tillbox.db
	DBDSN string `env:"TILLBOX_DB_DSN"`
	// 对外地址，例如 https://pay.example.com。拼收银台链接用；为空时按请求的 Host 推断
	PublicURL string `env:"TILLBOX_PUBLIC_URL"`
	// 可信反向代理的网段，逗号分隔。只有来自这些地址的请求才认 X-Forwarded-For / X-Forwarded-Proto
	TrustedProxies []string `env:"TILLBOX_TRUSTED_PROXIES" envSeparator:","`
	// 主密钥（32 字节的 Base64）。为空时读 DataDir/master.key，没有就生成一个
	MasterKey string `env:"TILLBOX_MASTER_KEY"`
	// 本地演示模式：可以建演示渠道、模拟付款，不连真实的收款渠道。只用于本机演示和开发
	Demo bool `env:"TILLBOX_DEMO"`
	// 日志级别：debug / info / warn / error
	LogLevel string `env:"TILLBOX_LOG_LEVEL" envDefault:"info"`

	TrustedPrefixes []netip.Prefix `env:"-"`
}

func Load() (*Config, error) {
	var c Config
	if err := env.Parse(&c); err != nil {
		return nil, err
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) normalize() error {
	switch c.DBDriver {
	case "sqlite":
		if c.DBDSN == "" {
			c.DBDSN = filepath.Join(c.DataDir, "tillbox.db")
		}
	case "postgres":
		if c.DBDSN == "" {
			return fmt.Errorf("TILLBOX_DB_DRIVER=postgres 时必须设置 TILLBOX_DB_DSN")
		}
	default:
		return fmt.Errorf("TILLBOX_DB_DRIVER 只能是 sqlite 或 postgres，收到 %q", c.DBDriver)
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("TILLBOX_PUBLIC_URL 不是合法的 http(s) 地址：%q", c.PublicURL)
		}
		c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	}
	for _, s := range c.TrustedProxies {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			addr, err := netip.ParseAddr(s)
			if err != nil {
				return fmt.Errorf("TILLBOX_TRUSTED_PROXIES 里的 %q 不是 IP 或网段", s)
			}
			c.TrustedPrefixes = append(c.TrustedPrefixes, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return fmt.Errorf("TILLBOX_TRUSTED_PROXIES 里的 %q 不是 IP 或网段", s)
		}
		c.TrustedPrefixes = append(c.TrustedPrefixes, p.Masked())
	}
	return nil
}
