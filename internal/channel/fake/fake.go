// Package fake 是演示渠道（渠道类型 demo）：流水存在内存里，靠模拟付款接口注入，不连支付宝。
// 只在 TILLBOX_DEMO=true 时可以使用，给本地演示和测试用。
package fake

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/store"
)

type Channel struct {
	channel.Base
	mu      sync.Mutex
	entries []channel.Entry
	seq     int
	// FailWith 不为空时，Fetch 返回这个错误（测试用）。
	FailWith error
	// Allowed 为真表示在演示模式下运行；正式模式下演示渠道不能收款。
	Allowed bool
}

func New() *Channel { return &Channel{} }

func (c *Channel) Configure(row store.Channel) { c.SetRow(row) }
func (c *Channel) PayTypes() []string          { return []string{"alipay"} }
func (c *Channel) MaxSpan() time.Duration      { return 24 * time.Hour }
func (c *Channel) Ready(context.Context) error {
	if !c.Allowed {
		return errors.New("演示渠道只能在本地演示模式（TILLBOX_DEMO=true）下使用")
	}
	return c.CheckEnabled()
}

func (c *Channel) Guide(_ context.Context, o *store.Order) (channel.Guide, error) {
	return channel.Guide{
		QRCode:       "TILLBOX-FAKE-" + o.TradeNo,
		PayeeName:    "演示收款方",
		App:          "演示渠道",
		ManualAmount: true,
	}, nil
}

// Pay 模拟一笔到账，返回流水号。
func (c *Channel) Pay(amount int64, at time.Time, payer string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	id := fmt.Sprintf("FAKE%d%06d", at.Unix(), c.seq)
	c.entries = append(c.entries, channel.Entry{
		ExternalID: id, Direction: store.DirIn, Amount: amount, OccurredAt: at,
		AlipayOrderNo: fmt.Sprintf("2026%016d", c.seq), OtherAccount: payer, Memo: "模拟付款", Kind: "在线支付",
		Raw: fmt.Sprintf(`{"account_log_id":%q,"trans_amount":"%d.%02d"}`, id, amount/100, amount%100),
	})
	return id
}

// Add 直接放一条流水（测试用）。
func (c *Channel) Add(e channel.Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
}

func (c *Channel) Fetch(_ context.Context, start, end time.Time) ([]channel.Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.FailWith != nil {
		return nil, c.FailWith
	}
	var out []channel.Entry
	for _, e := range c.entries {
		if !e.OccurredAt.Before(start) && e.OccurredAt.Before(end) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OccurredAt.Before(out[j].OccurredAt) })
	return out, nil
}

// Test 演示渠道不连外部服务，就绪即通过。
func (c *Channel) Test(ctx context.Context) error { return c.Ready(ctx) }
