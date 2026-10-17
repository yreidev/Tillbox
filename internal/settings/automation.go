package settings

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// Automation uses a configurable Chat Completions-compatible endpoint.
// APIKey is returned only inside the server and encrypted in settings storage.
type Automation struct {
	Enabled         bool   `json:"enabled"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	APIKey          string `json:"api_key,omitempty"`
	AutoRecheck     bool   `json:"auto_recheck"`
	AutoNotify      bool   `json:"auto_notify"`
	AutoLink        bool   `json:"auto_link"`
	IntervalSeconds int    `json:"interval_seconds"`
	MaxAttempts     int    `json:"max_attempts"`
}

func (s *Service) Automation(ctx context.Context) (Automation, error) {
	v := Automation{AutoRecheck: true, AutoNotify: true, AutoLink: true, IntervalSeconds: 60, MaxAttempts: 3}
	_, err := s.load(ctx, "automation", &v)
	if err == nil && v.APIKey != "" {
		v.APIKey, err = s.box.Open(v.APIKey)
	}
	return v, err
}

func (s *Service) SaveAutomation(ctx context.Context, v Automation) error {
	v.BaseURL = strings.TrimRight(strings.TrimSpace(v.BaseURL), "/")
	v.Model = strings.TrimSpace(v.Model)
	if v.IntervalSeconds < 30 || v.IntervalSeconds > 3600 || v.MaxAttempts < 1 || v.MaxAttempts > 10 {
		return errors.New("巡检间隔须为 30–3600 秒，单项尝试上限须为 1–10 次")
	}
	if v.BaseURL != "" {
		u, err := url.Parse(v.BaseURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
			return errors.New("模型接口须为 HTTPS Base URL（本机测试可用 HTTP），不能带凭据、查询或片段")
		}
	}
	if len(v.Model) > 150 || len(v.APIKey) > 4096 || len(v.BaseURL) > 1000 {
		return errors.New("模型配置过长")
	}
	if v.Enabled && (v.BaseURL == "" || v.Model == "") {
		return errors.New("启用自动处理前请填写模型接口和模型名称")
	}
	if v.APIKey != "" {
		sealed, err := s.box.Seal(v.APIKey)
		if err != nil {
			return err
		}
		v.APIKey = sealed
	}
	return s.save(ctx, "automation", v)
}
