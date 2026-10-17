package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/store"
)

// 业务消息：收款到账、付款人提交申诉。它们和引起它的改动在同一个事务里排进 admin_events，
// 这里按设置的开关拼成消息发出去；发送失败的留着，下一轮重发。

const (
	// maxEvents 是一轮最多处理的业务消息条数，多的下一轮接着发。
	maxEvents = 50
	// maxListed 是一条消息里最多逐笔列出的条数，其余只计数。
	maxListed = 10
	// KeepEvents 是发完或跳过的业务消息在库里保留的时长。
	KeepEvents = 7 * 24 * time.Hour
)

// claimKinds 和付款人自助页的申诉类型一致。
var claimKinds = map[string]string{
	"not_received": "已付款未到账",
	"wrong_amount": "金额付错",
	"late":         "迟付",
	"duplicate":    "重复付款",
	"other":        "其他问题",
}

// Wake 让业务消息马上发一轮（订单到账、付款人提交申诉后调用）。Run 还没开始时不用唤醒，Run 开始时会先发一轮。
func (s *Service) Wake() {
	select {
	case s.wakeCh() <- struct{}{}:
	default:
	}
}

func (s *Service) wakeCh() chan struct{} {
	s.wakeOnce.Do(func() { s.wake = make(chan struct{}, 1) })
	return s.wake
}

func (s *Service) siteName(ctx context.Context) string {
	site, err := s.Settings.Site(ctx)
	if err != nil || site.Name == "" {
		return "Tillbox"
	}
	return site.Name
}

// Deliver 发出排着的业务消息，同一类合成一条。没开这类消息、没配好 Telegram、订单或申诉已经不在的不发，记为跳过。
func (s *Service) Deliver(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	nowMs := store.Ms(now)
	if _, err := s.DB.NewDelete().Model((*store.AdminEvent)(nil)).
		Where("status <> ?", store.AdminEventPending).Where("done_at < ?", store.Ms(now.Add(-KeepEvents))).Exec(ctx); err != nil {
		return err
	}
	var evs []store.AdminEvent
	if err := s.DB.NewSelect().Model(&evs).Where("status = ?", store.AdminEventPending).Order("id ASC").Limit(maxEvents).Scan(ctx); err != nil {
		return err
	}
	if len(evs) == 0 {
		return nil
	}
	cfg, err := s.Settings.Telegram(ctx)
	if err != nil {
		return err
	}
	chats, err := s.DB.NewSelect().Model((*store.TelegramChat)(nil)).Count(ctx)
	if err != nil {
		return err
	}
	on := map[string]bool{store.AdminEventPaid: cfg.Paid, store.AdminEventClaim: cfg.Claims}
	byKind := map[string][]store.AdminEvent{}
	var skipped []int64
	for _, e := range evs {
		if cfg.Token != "" && chats > 0 && on[e.Kind] {
			byKind[e.Kind] = append(byKind[e.Kind], e)
		} else {
			skipped = append(skipped, e.ID)
		}
	}
	state, err := s.State(ctx)
	if err != nil {
		return err
	}
	var sent []int64
	for _, kind := range []string{store.AdminEventPaid, store.AdminEventClaim} {
		group := byKind[kind]
		if len(group) == 0 {
			continue
		}
		var msg Message
		var included []int64
		if kind == store.AdminEventPaid {
			msg, included, err = s.paidMessage(ctx, group, now)
		} else {
			msg, included, err = s.claimMessage(ctx, group, now)
		}
		if err != nil {
			return err
		}
		skipped = append(skipped, without(ids(group), included)...)
		if len(included) == 0 {
			continue
		}
		err := s.Telegram.Send(ctx, msg)
		switch {
		case errors.Is(err, ErrNoChats):
			skipped = append(skipped, included...)
		case err != nil:
			state.SendErrorAt, state.SendError = nowMs, err.Error()
			slog.Warn("发送业务消息失败", "kind", kind, "err", err)
		default:
			state.SentAt = nowMs
			sent = append(sent, included...)
		}
	}
	for status, list := range map[string][]int64{store.AdminEventSent: sent, store.AdminEventSkipped: skipped} {
		if len(list) == 0 {
			continue
		}
		if _, err := s.DB.NewUpdate().Model((*store.AdminEvent)(nil)).Set("status = ?", status).Set("done_at = ?", nowMs).
			Where("id IN (?)", bun.List(list)).Exec(ctx); err != nil {
			return err
		}
	}
	return s.Settings.SaveRaw(ctx, stateKey, state)
}

func ids(evs []store.AdminEvent) []int64 {
	out := make([]int64, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

func without(all, some []int64) []int64 {
	in := make(map[int64]bool, len(some))
	for _, id := range some {
		in[id] = true
	}
	var out []int64
	for _, id := range all {
		if !in[id] {
			out = append(out, id)
		}
	}
	return out
}

// eventOf 按对象编号找回业务消息编号。
func eventOf(evs []store.AdminEvent) (refs []int64, byRef map[int64]int64) {
	byRef = make(map[int64]int64, len(evs))
	for _, e := range evs {
		refs = append(refs, e.RefID)
		byRef[e.RefID] = e.ID
	}
	return refs, byRef
}

// paidMessage 把到账的订单拼成一条消息：实付金额、商户、渠道、商品名、订单号。不带付款人信息。
// 已经不是“已支付”的订单（例如管理员撤销了关联）不发。
func (s *Service) paidMessage(ctx context.Context, evs []store.AdminEvent, now time.Time) (Message, []int64, error) {
	refs, byRef := eventOf(evs)
	var orders []store.Order
	if err := s.DB.NewSelect().Model(&orders).Where("id IN (?)", bun.List(refs)).Where("status = ?", store.OrderPaid).
		Order("paid_at ASC", "id ASC").Scan(ctx); err != nil {
		return Message{}, nil, err
	}
	if len(orders) == 0 {
		return Message{}, nil, nil
	}
	var merchants []store.Merchant
	if err := s.DB.NewSelect().Model(&merchants).Column("id", "name").Scan(ctx); err != nil {
		return Message{}, nil, err
	}
	var channels []store.Channel
	if err := s.DB.NewSelect().Model(&channels).Column("id", "name").Scan(ctx); err != nil {
		return Message{}, nil, err
	}
	merchantName := make(map[int64]string, len(merchants))
	for _, m := range merchants {
		merchantName[m.ID] = m.Name
	}
	channelName := make(map[string]string, len(channels))
	for _, c := range channels {
		channelName[c.Key()] = c.Name
	}
	included := make([]int64, 0, len(orders))
	items := make([]paidItem, 0, len(orders))
	for _, o := range orders {
		included = append(included, byRef[o.ID])
		m := merchantName[o.MerchantID]
		if m == "" {
			m = fmt.Sprintf("商户 %d", o.MerchantID)
		}
		c := channelName[o.Channel]
		if c == "" {
			c = o.Channel
		}
		items = append(items, paidItem{Paid: o.PaidAmount, Amount: o.Amount, Merchant: m, Channel: c, Name: o.Name, TradeNo: o.TradeNo, Test: o.IsTest})
	}
	link := s.button("查看已支付订单", "/admin/orders?state=paid")
	if len(orders) == 1 {
		link = s.button("查看订单", "/admin/orders?q="+orders[0].TradeNo)
	}
	return render(paidCard(items), s.siteName(ctx), now, false, link), included, nil
}

// claimMessage 把付款人提交的申诉拼成一条消息：类型、申报金额、订单号。付款人填的联系方式和说明不发，到后台看。
func (s *Service) claimMessage(ctx context.Context, evs []store.AdminEvent, now time.Time) (Message, []int64, error) {
	refs, byRef := eventOf(evs)
	var cases []store.AssistanceCase
	if err := s.DB.NewSelect().Model(&cases).Where("id IN (?)", bun.List(refs)).Order("id ASC").Scan(ctx); err != nil {
		return Message{}, nil, err
	}
	if len(cases) == 0 {
		return Message{}, nil, nil
	}
	var orderIDs []int64
	for _, c := range cases {
		if c.OrderID > 0 {
			orderIDs = append(orderIDs, c.OrderID)
		}
	}
	tradeNo := map[int64]string{}
	if len(orderIDs) > 0 {
		var orders []store.Order
		if err := s.DB.NewSelect().Model(&orders).Column("id", "trade_no").Where("id IN (?)", bun.List(orderIDs)).Scan(ctx); err != nil {
			return Message{}, nil, err
		}
		for _, o := range orders {
			tradeNo[o.ID] = o.TradeNo
		}
	}
	included := make([]int64, 0, len(cases))
	items := make([]claimItem, 0, len(cases))
	for _, c := range cases {
		included = append(included, byRef[c.ID])
		items = append(items, claimItem{Kind: c.Kind, Amount: c.ClaimAmount, TradeNo: tradeNo[c.OrderID]})
	}
	return render(claimCard(items), s.siteName(ctx), now, false, s.button("处理申诉", "/admin/assistance")), included, nil
}
