package assistance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/confirm"
	"tillbox/internal/ledger"
	"tillbox/internal/notify"
	"tillbox/internal/order"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

type Service struct {
	DB       *bun.DB
	Settings *settings.Service
	Orders   *order.Service
	Ledger   *ledger.Ledger
	Pollers  *ledger.Pollers
	Confirm  *confirm.Service
	Notifier *notify.Worker
	Client   *http.Client
	mu       sync.Mutex
	actionMu sync.Mutex
	claimMu  sync.Mutex
	wake     chan struct{}
	// OnClaim 在付款人提交申诉后调用（用来唤醒管理员消息的队列）。
	OnClaim func()
}

type State struct {
	LastRunAt int64  `json:"last_run_at"`
	LastOKAt  int64  `json:"last_ok_at"`
	LastError string `json:"last_error"`
	Processed int    `json:"processed"`
}

func New(db *bun.DB, st *settings.Service, os *order.Service, l *ledger.Ledger, p *ledger.Pollers, cf *confirm.Service, n *notify.Worker) *Service {
	return &Service{DB: db, Settings: st, Orders: os, Ledger: l, Pollers: p, Confirm: cf, Notifier: n, Client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, wake: make(chan struct{}, 1)}
}
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("AI 自动处理巡检失败", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}
func (s *Service) Status(ctx context.Context) (State, error) {
	var v State
	_, err := s.Settings.Raw(ctx, "automation_state", &v)
	return v, err
}

func (s *Service) discover(ctx context.Context) error {
	var as []store.Anomaly
	if err := s.DB.NewSelect().Model(&as).Where("status = ?", store.AnomalyOpen).
		Where("NOT EXISTS (SELECT 1 FROM assistance_cases ac WHERE ac.dedupe_key = 'anomaly:' || CAST(an.id AS TEXT))").Order("an.id ASC").Limit(25).Scan(ctx); err != nil {
		return err
	}
	for _, a := range as {
		if err := s.add(ctx, &store.AssistanceCase{DedupeKey: fmt.Sprintf("anomaly:%d", a.ID), OrderID: a.OrderID, TransactionID: a.TransactionID, AnomalyID: a.ID, Source: "ledger", Kind: a.Kind}); err != nil {
			return err
		}
	}
	var ns []store.Notification
	if err := s.DB.NewSelect().Model(&ns).Where("status = ?", store.NotifyDead).
		Where("NOT EXISTS (SELECT 1 FROM assistance_cases ac WHERE ac.dedupe_key = 'notification:' || CAST(n.order_id AS TEXT))").Order("n.order_id ASC").Limit(25).Scan(ctx); err != nil {
		return err
	}
	for _, n := range ns {
		if err := s.add(ctx, &store.AssistanceCase{DedupeKey: fmt.Sprintf("notification:%d", n.OrderID), OrderID: n.OrderID, Source: "notification", Kind: "notification_failed"}); err != nil {
			return err
		}
	}
	return nil
}

// settle closes open cases, manual ones included, once the underlying problem
// was handled elsewhere: the anomaly was resolved on the ledger page, the
// notification was delivered, or the payer's order was paid and delivered.
// A merchant may acknowledge a notification for an order it already expired
// without crediting it, so an expired order recovered here goes to an
// administrator instead of closing on delivery.
func (s *Service) settle(ctx context.Context) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	const delivered = "ac.order_id IN (SELECT o.id FROM orders o LEFT JOIN notifications n ON n.order_id = o.id WHERE o.status = ? AND (n.status = ? OR o.notify_url = ''))"
	const recoveredLate = "EXISTS (SELECT 1 FROM assistance_actions aa WHERE aa.case_id = ac.id AND aa.action = 'link_late')"
	var late []store.AssistanceCase
	err := s.DB.NewSelect().Model(&late).
		Where("ac.status IN (?)", bun.List([]string{"pending", "waiting", "manual"})).
		Where("ac.source = 'payer'").Where(recoveredLate).
		Where("NOT EXISTS (SELECT 1 FROM assistance_actions aa WHERE aa.case_id = ac.id AND aa.action = 'check_merchant')").
		Where(delivered, store.OrderPaid, store.NotifySuccess).
		Order("ac.id ASC").Limit(50).Scan(ctx)
	if err != nil {
		return err
	}
	for i := range late {
		if err = s.finish(ctx, &late[i], "manual", "过期订单已补认，商户通知已送达，需核对商户是否入账", "已确认到账，订单结果已发送至商户，正在核对商户入账情况。", "check_merchant", "部分商户系统对已过期订单回 success 也不入账，请到商户后台核对该订单，未入账时手动补充，再在本页结案"); err != nil {
			return err
		}
	}
	var cs []store.AssistanceCase
	err = s.DB.NewSelect().Model(&cs).
		Where("ac.status IN (?)", bun.List([]string{"pending", "waiting", "manual"})).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("ac.source = 'ledger' AND ac.anomaly_id IN (SELECT id FROM anomalies WHERE status = ?)", store.AnomalyResolved).
				WhereOr("ac.source = 'notification' AND ac.order_id IN (SELECT order_id FROM notifications WHERE status = ?)", store.NotifySuccess).
				WhereOr("ac.source = 'payer' AND ac.kind IN ('not_received', 'late') AND NOT "+recoveredLate+" AND "+delivered, store.OrderPaid, store.NotifySuccess)
		}).Order("ac.id ASC").Limit(50).Scan(ctx)
	if err != nil {
		return err
	}
	for i := range cs {
		c := &cs[i]
		switch c.Source {
		case "ledger":
			err = s.finish(ctx, c, "resolved", "对应流水异常已处理", "相关付款记录已完成核查。", "verify", "流水异常已在别处处理，自动结案")
		case "notification":
			err = s.finish(ctx, c, "resolved", "通知已送达", "已确认到账，商户通知已送达。", "verify", "通知已送达，自动结案")
		default:
			err = s.finish(ctx, c, "resolved", "订单已支付，通知已送达", "已确认到账。订单结果已发送至商户；余额等业务问题请联系商户。", "verify", "订单已到账且通知已送达，自动结案")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) add(ctx context.Context, c *store.AssistanceCase) error {
	now := store.Now()
	c.Status = "pending"
	c.NextAt = now
	c.CreatedAt = now
	c.UpdatedAt = now
	c.PublicReply = "已收到，等待核查付款及订单状态。"
	return s.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(c).On("CONFLICT (dedupe_key) DO NOTHING").Exec(ctx); err != nil {
			return err
		}
		// 付款人提交的申诉同时排一条给管理员的消息。
		if c.Source != "payer" || c.ID == 0 {
			return nil
		}
		return store.QueueAdminEvent(ctx, tx, store.AdminEventClaim, c.ID)
	})
}

func (s *Service) RunOnce(ctx context.Context) (err error) {
	if !s.mu.TryLock() {
		return nil
	}
	defer s.mu.Unlock()
	// Closing cases whose underlying problem is gone needs no model, so it also
	// runs while automation is disabled.
	if err = s.settle(ctx); err != nil {
		return err
	}
	cfg, err := s.Settings.Automation(ctx)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	state, err := s.Status(ctx)
	if err != nil {
		return err
	}
	if store.Now()-state.LastRunAt < int64(cfg.IntervalSeconds)*1000 {
		return nil
	}
	state.LastRunAt = store.Now()
	state.Processed = 0
	defer func() {
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			state.LastError = err.Error()
		} else {
			state.LastError = ""
			state.LastOKAt = store.Now()
		}
		if e := s.Settings.SaveRaw(ctx, "automation_state", state); e != nil && err == nil {
			err = e
		}
	}()
	if err = s.discover(ctx); err != nil {
		return err
	}
	var cases []store.AssistanceCase
	if err = s.DB.NewSelect().Model(&cases).Where("status IN (?)", bun.List([]string{"pending", "waiting"})).Where("next_at <= ?", store.Now()).Order("next_at ASC", "ac.id ASC").Limit(5).Scan(ctx); err != nil {
		return err
	}
	var lastError error
	for i := range cases {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e := s.process(ctx, &cases[i], cfg); e != nil {
			lastError = e
		}
		state.Processed++
	}
	return lastError
}

// facts are an allowlist: no keys, signed URLs, raw ledger payloads, contacts,
// buyer accounts or callback response bodies leave the server.
func (s *Service) process(ctx context.Context, c *store.AssistanceCase, cfg settings.Automation) error {
	s.actionMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.actionMu.Unlock()
		}
	}()
	var fresh store.AssistanceCase
	if err := s.DB.NewSelect().Model(&fresh).Where("id = ?", c.ID).Scan(ctx); err != nil {
		return err
	}
	if (fresh.Status != "pending" && fresh.Status != "waiting") || fresh.NextAt > store.Now() {
		return nil
	}
	*c = fresh
	current, err := s.Settings.Automation(ctx)
	if err != nil {
		return err
	}
	if !current.Enabled {
		return nil
	}
	cfg = current
	if c.Source == "payer" && c.OrderID == 0 {
		s.claimMu.Lock()
		err := s.findClaimOrder(ctx, c)
		s.claimMu.Unlock()
		if err != nil || c.OrderID == 0 {
			return err
		}
	}
	financialIssue := c.Source == "ledger" || (c.Source == "payer" && c.Kind != "not_received" && c.Kind != "late")
	var o *store.Order
	if c.OrderID > 0 {
		var err error
		o, err = s.Orders.ByID(ctx, c.OrderID)
		if err != nil {
			return err
		}
	}
	var n store.Notification
	nerr := s.DB.NewSelect().Model(&n).Where("order_id = ?", c.OrderID).Scan(ctx)
	if nerr != nil && !errors.Is(nerr, sql.ErrNoRows) {
		return nerr
	}
	if o != nil && o.Status == store.OrderPaid && (n.Status == store.NotifySuccess || o.NotifyURL == "") && !financialIssue {
		return s.finish(ctx, c, "resolved", "订单已支付，通知已送达", "已确认到账。订单结果已发送至商户；余额等业务问题请联系商户。", "verify", "当前账务状态已确认")
	}
	if c.Source == "ledger" && c.AnomalyID > 0 {
		var a store.Anomaly
		if err := s.DB.NewSelect().Model(&a).Where("id = ?", c.AnomalyID).Scan(ctx); err != nil {
			return err
		}
		if a.Status == store.AnomalyResolved {
			return s.finish(ctx, c, "resolved", "对应流水异常已处理", "相关付款记录已完成核查。", "verify", "异常状态已更新")
		}
	}
	if c.Attempts >= cfg.MaxAttempts {
		return s.finish(ctx, c, "manual", "达到自动处理尝试上限", "已转人工核查，请保留付款凭证。", "limit", "单项尝试上限")
	}
	var t store.Transaction
	if c.PaymentRef != "" && o != nil {
		var ts []store.Transaction
		err := s.DB.NewSelect().Model(&ts).Where("channel = ?", o.Channel).
			WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("alipay_order_no = ?", c.PaymentRef).WhereOr("external_id = ?", c.PaymentRef)
			}).Limit(2).Scan(ctx)
		if err != nil {
			return err
		}
		if len(ts) == 1 {
			t = ts[0]
		}
	} else if c.TransactionID > 0 {
		if err := s.DB.NewSelect().Model(&t).Where("id = ?", c.TransactionID).Scan(ctx); err != nil {
			return err
		}
	}
	allowed := []string{"manual"}
	if cfg.AutoRecheck && !financialIssue && o != nil && (o.Status == store.OrderPending || o.Status == store.OrderExpired) {
		allowed = append(allowed, "recheck")
	}
	if cfg.AutoNotify && !financialIssue && o != nil && o.Status == store.OrderPaid && nerr == nil && n.Status != store.NotifySuccess && store.Now()-n.LastAt >= 60_000 {
		allowed = append(allowed, "retry_notification")
	}
	// 补认只适用于经营码这类靠流水推断的渠道；第三方易支付渠道的付款以第三方平台确认为准。
	if cfg.AutoLink && !financialIssue && o != nil && c.Source == "payer" && s.Pollers.Get(o.Channel) != nil && t.ID > 0 && c.PaymentRef != "" && c.ClaimAmount == o.PayAmount && t.Amount == o.PayAmount && t.OrderID == 0 && (o.Status == store.OrderPending || o.Status == store.OrderExpired) {
		allowed = append(allowed, "link_payment")
	}
	if len(allowed) == 1 {
		// Only manual review remains, so a model request could not change the outcome.
		if cfg.AutoNotify && !financialIssue && o != nil && o.Status == store.OrderPaid && nerr == nil && n.Status != store.NotifySuccess {
			return s.finish(ctx, c, "waiting", "通知刚发送过", "已确认到账，商户通知正在处理中，请稍后查询。", "wait", "距上次通知不足一分钟，稍后复核；未调用模型")
		}
		return s.finish(ctx, c, "manual", "没有可自动执行的操作", "已转人工核查，请保留付款凭证。", "manual", "只能人工处理；未调用模型")
	}
	facts := map[string]any{"kind": c.Kind, "source": c.Source, "user_message": c.Message, "attempts": c.Attempts, "allowed_actions": allowed, "notification_status": n.Status}
	if o != nil {
		facts["order"] = map[string]any{"state": order.Effective(o, time.Now()), "amount_cents": o.Amount, "pay_amount_cents": o.PayAmount, "created_at": o.CreatedAt, "expires_at": o.ExpiresAt}
	}
	if t.ID > 0 {
		facts["ledger"] = map[string]any{"direction": t.Direction, "amount_cents": t.Amount, "occurred_at": t.OccurredAt, "state": t.State, "already_linked": t.OrderID != 0, "payer_reference_matches": c.PaymentRef != "" && (c.PaymentRef == t.ExternalID || c.PaymentRef == t.AlipayOrderNo)}
	}
	c.Attempts++
	c.UpdatedAt = max(store.Now(), c.UpdatedAt+1)
	// Persist the attempt before any external call; crashes cannot bypass retry limits.
	if _, err := s.DB.NewUpdate().Model(c).Column("attempts", "updated_at").WherePK().Exec(ctx); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	locked = false
	s.actionMu.Unlock()
	d, err := s.decide(callCtx, cfg, facts)
	cancel()
	s.actionMu.Lock()
	locked = true
	var liveCase store.AssistanceCase
	if e := s.DB.NewSelect().Model(&liveCase).Where("id = ?", c.ID).Scan(ctx); e != nil {
		return e
	}
	if liveCase.UpdatedAt != c.UpdatedAt || liveCase.Status != c.Status {
		return nil
	}
	currentConfig, e := s.Settings.Automation(ctx)
	if e != nil {
		return e
	}
	if !currentConfig.Enabled {
		return s.finish(ctx, c, "waiting", "自动处理已停用", "等待核查。", "paused", "模型调用期间管理员关闭了自动处理")
	}
	if (d.Action == "recheck" && !currentConfig.AutoRecheck) || (d.Action == "retry_notification" && !currentConfig.AutoNotify) || (d.Action == "link_payment" && !currentConfig.AutoLink) {
		return s.finish(ctx, c, "manual", d.Reason, "已转人工核查。", "blocked", "管理员已收回对应操作权限")
	}
	if err != nil {
		status := "waiting"
		if c.Attempts >= cfg.MaxAttempts {
			status = "manual"
		}
		if e := s.finish(ctx, c, status, err.Error(), "核查暂未完成，已保留记录等待重试或人工处理。", "model_error", err.Error()); e != nil {
			return e
		}
		return err
	}
	if !slices.Contains(allowed, d.Action) {
		return s.finish(ctx, c, "manual", d.Reason, "已转人工核查，请保留付款凭证。", "blocked", "模型请求的动作不在当前授权范围内")
	}
	switch d.Action {
	case "recheck":
		if cf, _ := s.Confirm.Confirmer(o.Channel); cf != nil {
			s.Confirm.Kick(o.ID)
			return s.finish(ctx, c, "waiting", d.Reason, "已请求向支付通道查询这笔订单，正在等待结果。", d.Action, "已触发第三方平台查单")
		}
		s.Pollers.Kick(o.Channel)
		return s.finish(ctx, c, "waiting", d.Reason, "已请求重新查账，正在等待真实到账记录。", d.Action, "已触发查账，仍受支付宝查账间隔限制")
	case "retry_notification":
		// Recheck the live state after the model request.
		live, e := s.Orders.ByID(ctx, o.ID)
		if e != nil {
			return e
		}
		if live.Status != store.OrderPaid {
			return s.finish(ctx, c, "manual", d.Reason, "已转人工核查。", "blocked", "订单状态已变化")
		}
		var current store.Notification
		if e = s.DB.NewSelect().Model(&current).Where("order_id = ?", o.ID).Scan(ctx); e != nil {
			return e
		}
		if current.Status == store.NotifySuccess {
			return s.finish(ctx, c, "resolved", d.Reason, "已确认到账，商户通知已送达。", d.Action, "通知已由其他任务送达")
		}
		if store.Now()-current.LastAt < 60_000 {
			return s.finish(ctx, c, "waiting", d.Reason, "通知正在处理中，请稍后查询。", d.Action, "距上次通知不足一分钟，已合并重试")
		}
		attempt, e := s.Notifier.Resend(ctx, o.ID)
		if e == nil && attempt != nil && attempt.Success {
			return s.finish(ctx, c, "resolved", d.Reason, "已确认到账并补发商户通知。", d.Action, "补发成功")
		}
		return s.finish(ctx, c, "waiting", d.Reason, "已确认到账，商户通知仍在重试。", d.Action, "补发未成功，等待重试或人工处理")
	case "link_payment":
		s.claimMu.Lock()
		defer s.claimMu.Unlock()
		// Submissions and proof consumption use the same lock.
		conflict, e := s.DB.NewSelect().Model((*store.AssistanceCase)(nil)).Where("payment_ref = ?", c.PaymentRef).Where("source = ?", "payer").Where("order_id > 0").Where("order_id <> ?", c.OrderID).Exists(ctx)
		if e != nil {
			return e
		}
		if conflict {
			return s.finish(ctx, c, "manual", d.Reason, "付款凭证存在归属争议，已转人工核查。", "blocked", "同一付款凭证被提交至其他订单")
		}
		// The merchant may have expired its own order by now; settle asks an
		// administrator to confirm the credit once the notification is delivered.
		late := order.Effective(o, time.Now()) == store.OrderExpired
		if e = s.Ledger.LinkVerified(ctx, t.ID, o.ID, c.PaymentRef, c.ClaimAmount, c.PaidAt); e != nil {
			return s.finish(ctx, c, "manual", d.Reason, "付款证据需要人工核查，请保留支付宝账单。", "blocked", e.Error())
		}
		if late {
			return s.finish(ctx, c, "waiting", d.Reason, "已根据真实流水确认到账，正在向商户发送结果。", "link_late", "真实流水通过校验，已补认过期订单并进入通知队列；通知送达后转人工核对商户是否入账")
		}
		return s.finish(ctx, c, "waiting", d.Reason, "已根据真实流水确认到账，正在向商户发送结果。", d.Action, "真实流水通过校验，已补认到账并进入通知队列")
	default:
		return s.finish(ctx, c, "manual", d.Reason, "已转人工核查，请保留付款凭证。", d.Action, "模型判定需要人工处理")
	}
}

func (s *Service) finish(ctx context.Context, c *store.AssistanceCase, status, summary, reply, action, result string) error {
	if status == "waiting" {
		cfg, err := s.Settings.Automation(ctx)
		if err != nil {
			return err
		}
		// Re-checked after one interval; the model is asked again only while attempts remain.
		c.NextAt = store.Now() + int64(cfg.IntervalSeconds)*1000
	}
	c.Status = status
	c.Summary = limit(summary, 1000)
	c.PublicReply = reply
	c.UpdatedAt = store.Now()
	return s.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model(c).Column("status", "summary", "public_reply", "next_at", "updated_at").WherePK().Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewInsert().Model(&store.AssistanceAction{CaseID: c.ID, Action: action, Reason: c.Summary, Result: limit(result, 1000), CreatedAt: store.Now()}).Exec(ctx); err != nil {
			return err
		}
		return store.Audit(ctx, tx, "AI 自动处理", "assistance."+action, fmt.Sprint(c.ID), limit(result, 1000))
	})
}

func (s *Service) Test(ctx context.Context) (Decision, error) {
	cfg, err := s.Settings.Automation(ctx)
	if err != nil {
		return Decision{}, err
	}
	if cfg.BaseURL == "" || cfg.Model == "" {
		return Decision{}, errors.New("请先保存模型接口和模型名称")
	}
	d, err := s.decide(ctx, cfg, map[string]any{"kind": "connection_test", "allowed_actions": []string{"manual"}, "instruction": "本次仅测试连接，请返回 action=manual"})
	if err == nil && d.Action != "manual" {
		return Decision{}, errors.New("模型未遵守测试动作范围，请检查所选模型")
	}
	return d, err
}
