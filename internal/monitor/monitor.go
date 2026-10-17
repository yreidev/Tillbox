package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/epay"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

const (
	// Every 是检查的间隔。
	Every = 30 * time.Second
	// ResolveAfter：问题消失这么久才算恢复，避免在阈值附近来回跳时反复通知。
	ResolveAfter = 2 * time.Minute
	// KeepResolved 是已恢复的告警在库里保留的时长。
	KeepResolved = 180 * 24 * time.Hour
	stateKey     = "monitor_state"
)

// State 是监控任务自己的运行情况，存在设置表里，宿主机巡检据此判断告警任务是否还在跑。
type State struct {
	LastRunAt   int64  `json:"last_run_at"`
	LastError   string `json:"last_error"`
	SentAt      int64  `json:"sent_at"` // 上次成功发出告警消息
	SendErrorAt int64  `json:"send_error_at"`
	SendError   string `json:"send_error"`
}

type Service struct {
	DB       *bun.DB
	Settings *settings.Service
	Checker  *Checker
	Telegram *Telegram
	// PublicURL 是对外地址，告警消息末尾附上监控页链接；为空时不附。
	PublicURL string
	// Now 可以在测试里替换。
	Now func() time.Time

	mu sync.Mutex // 同一时刻只评估一次
}

func (s *Service) State(ctx context.Context) (State, error) {
	var v State
	_, err := s.Settings.Raw(ctx, stateKey, &v)
	return v, err
}

// Run 定时检查，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	s.Telegram.setBase(ctx)
	defer s.Telegram.CancelBind()
	t := time.NewTicker(Every)
	defer t.Stop()
	for {
		if err := s.Evaluate(ctx); err != nil && ctx.Err() == nil {
			slog.Error("监控检查失败", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Open 返回进行中的告警，严重的在前，同级按开始时间从新到旧。
func (s *Service) Open(ctx context.Context) ([]store.Alert, error) {
	alerts := make([]store.Alert, 0)
	err := s.DB.NewSelect().Model(&alerts).Where("resolved_at = 0").
		OrderExpr("CASE level WHEN ? THEN 0 ELSE 1 END", store.AlertCritical).Order("started_at DESC", "id DESC").Scan(ctx)
	return alerts, err
}

// Resolved 返回最近恢复的告警。
func (s *Service) Resolved(ctx context.Context, limit int) ([]store.Alert, error) {
	alerts := make([]store.Alert, 0)
	err := s.DB.NewSelect().Model(&alerts).Where("resolved_at > 0").Order("resolved_at DESC", "id DESC").Limit(int64(limit)).Scan(ctx)
	return alerts, err
}

// event 是这次要通知的一条告警变化。
type event struct {
	alert *store.Alert
	kind  string // open、more（笔数变多）、remind、resolved
}

// Evaluate 检查一遍，更新告警，有变化就发通知。
func (s *Service) Evaluate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	nowMs := store.Ms(now)
	state, err := s.State(ctx)
	if err != nil {
		return err
	}
	state.LastRunAt = nowMs
	problems, err := s.Checker.Check(ctx)
	if err != nil {
		state.LastError = err.Error()
		_ = s.Settings.SaveRaw(ctx, stateKey, state)
		return err
	}
	state.LastError = ""
	cfg, err := s.Settings.Telegram(ctx)
	if err != nil {
		return err
	}
	events, err := s.apply(ctx, problems, now, cfg)
	if err != nil {
		return err
	}
	if len(events) > 0 && cfg.Token != "" {
		err := s.Telegram.Send(ctx, s.message(ctx, events, now))
		switch {
		case errors.Is(err, ErrNoChats):
		case err != nil:
			state.SendErrorAt, state.SendError = nowMs, err.Error()
			slog.Warn("发送告警失败", "err", err)
		default:
			state.SentAt = nowMs
			var ids []int64
			for _, e := range events {
				if e.kind != "resolved" {
					ids = append(ids, e.alert.ID)
				}
			}
			if len(ids) > 0 {
				_, err := s.DB.NewUpdate().Model((*store.Alert)(nil)).Set("notified_at = ?", nowMs).Where("id IN (?)", bun.List(ids)).Exec(ctx)
				if err != nil {
					return err
				}
			}
		}
	}
	if _, err := s.DB.NewDelete().Model((*store.Alert)(nil)).
		Where("resolved_at > 0").Where("resolved_at < ?", store.Ms(now.Add(-KeepResolved))).Exec(ctx); err != nil {
		return err
	}
	return s.Settings.SaveRaw(ctx, stateKey, state)
}

// apply 按这次的检查结果开、改、关告警，返回要通知的变化。
func (s *Service) apply(ctx context.Context, problems []Problem, now time.Time, cfg settings.Telegram) ([]event, error) {
	nowMs := store.Ms(now)
	open, err := s.Open(ctx)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]*store.Alert, len(open))
	for i := range open {
		byKey[open[i].Key] = &open[i]
	}
	remind := time.Duration(cfg.RemindHours) * time.Hour
	var events []event
	for _, p := range problems {
		a := byKey[p.Key]
		if a == nil {
			a = &store.Alert{Key: p.Key, Level: p.Level, Title: p.Title, Detail: p.Detail, Link: p.Link, Count: p.Count, StartedAt: nowMs, LastSeenAt: nowMs}
			if _, err := s.DB.NewInsert().Model(a).Exec(ctx); err != nil {
				return nil, err
			}
			events = append(events, event{a, "open"})
			continue
		}
		delete(byKey, p.Key)
		more := p.Count > a.Count
		a.Level, a.Title, a.Detail, a.Link, a.Count, a.LastSeenAt = p.Level, p.Title, p.Detail, p.Link, p.Count, nowMs
		if _, err := s.DB.NewUpdate().Model(a).Column("level", "title", "detail", "link", "count", "last_seen_at").WherePK().Exec(ctx); err != nil {
			return nil, err
		}
		switch {
		case a.NotifiedAt == 0:
			events = append(events, event{a, "open"}) // 开始时没发出去，补发
		case more:
			events = append(events, event{a, "more"})
		case remind > 0 && now.Sub(store.Time(a.NotifiedAt)) >= remind:
			events = append(events, event{a, "remind"})
		}
	}
	for _, a := range byKey {
		if now.Sub(store.Time(a.LastSeenAt)) < ResolveAfter {
			continue
		}
		a.ResolvedAt = nowMs
		if _, err := s.DB.NewUpdate().Model(a).Column("resolved_at").WherePK().Exec(ctx); err != nil {
			return nil, err
		}
		if a.NotifiedAt > 0 {
			events = append(events, event{a, "resolved"})
		}
	}
	return events, nil
}

func duration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", max(1, int(d/time.Minute)))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d 小时", int(d/time.Hour))
	}
	return fmt.Sprintf("%d 天", int(d/(24*time.Hour)))
}

// message 把这次的变化拼成一条纯文本消息：新的和变多的在前，仍未恢复的其次，恢复的最后。
func (s *Service) message(ctx context.Context, events []event, now time.Time) string {
	site, err := s.Settings.Site(ctx)
	if err != nil || site.Name == "" {
		site.Name = "Tillbox"
	}
	lines := []string{fmt.Sprintf("[%s 告警] %s", site.Name, now.In(epay.CST).Format("01-02 15:04"))}
	icon := func(a *store.Alert) string {
		if a.Level == store.AlertCritical {
			return "🔴 "
		}
		return "🟠 "
	}
	withDetail := func(line string, a *store.Alert) {
		lines = append(lines, line)
		if a.Detail != "" {
			lines = append(lines, "    "+a.Detail)
		}
	}
	for _, e := range events {
		switch e.kind {
		case "open":
			withDetail(icon(e.alert)+e.alert.Title, e.alert)
		case "more":
			withDetail(icon(e.alert)+e.alert.Title+"（有新增）", e.alert)
		}
	}
	for _, e := range events {
		if e.kind == "remind" {
			lines = append(lines, fmt.Sprintf("⏰ 已持续 %s：%s", duration(now.Sub(store.Time(e.alert.StartedAt))), e.alert.Title))
		}
	}
	for _, e := range events {
		if e.kind == "resolved" {
			lines = append(lines, "🟢 已恢复："+e.alert.Title)
		}
	}
	if s.PublicURL != "" {
		lines = append(lines, "", "监控页："+s.PublicURL+"/admin/monitor")
	}
	return strings.Join(lines, "\n")
}

// Test 发一条测试消息。
func (s *Service) Test(ctx context.Context) error {
	site, err := s.Settings.Site(ctx)
	if err != nil {
		return err
	}
	name := site.Name
	if name == "" {
		name = "Tillbox"
	}
	err = s.Telegram.Send(ctx, fmt.Sprintf("[%s] 测试消息：告警会发到这里。", name))
	if err == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		state, serr := s.State(ctx)
		if serr != nil {
			return serr
		}
		state.SentAt = store.Ms(s.Now())
		return s.Settings.SaveRaw(ctx, stateKey, state)
	}
	return err
}

// OpenCount 返回进行中的告警数（侧栏角标用）。
func (s *Service) OpenCount(ctx context.Context) (int64, error) {
	return s.DB.NewSelect().Model((*store.Alert)(nil)).Where("resolved_at = 0").Count(ctx)
}
