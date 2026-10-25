// Package payroute 按商户的收款方式决定订单走哪个渠道：下单、收银台选支付方式和后台支付测试都用它。
// 商户每种支付方式选一个渠道；下单时没给支付方式、商户又开了不止一种时，订单先不定渠道，由付款人在收银台选，
// 选定后不能再换（只能取消订单重新下单）。
package payroute

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/channel/registry"
	"tillbox/internal/confirm"
	"tillbox/internal/order"
	"tillbox/internal/store"
)

// Method 是商户开放的一种支付方式和它走的渠道。
type Method struct {
	Type    string
	Channel channel.Instance // 渠道已删除时为 nil
	Row     store.Channel
	// Problem 是这种方式现在收不了款的原因（渠道删了、停用、没配好、不再开放这种方式），能收款时为空。
	Problem string
}

// Ready 判断这种方式现在能不能收款。
func (m Method) Ready() bool { return m.Problem == "" }

// Confirming 判断走的是不是逐单确认到账的渠道（第三方易支付）。这种渠道按原价付款，不加金额尾数。
func (m Method) Confirming() bool {
	_, ok := m.Channel.(channel.Confirmer)
	return ok
}

// CheckAmount 看金额有没有超出这种方式已知的单笔限额，超出时返回 *channel.AmountError。
func (m Method) CheckAmount(amount int64, now time.Time) error {
	l, ok := m.Channel.(channel.Limiter)
	if !ok {
		return nil
	}
	return l.CheckAmount(m.Type, amount, now)
}

// Methods 按支付方式的固定顺序列出商户开放的方式和它们现在能不能收款。
func Methods(ctx context.Context, chans *registry.Registry, routes map[string]int64) []Method {
	var out []Method
	for _, t := range channel.TypeOrder {
		id := routes[t]
		if id == 0 {
			continue
		}
		m := Method{Type: t}
		inst, row, ok := chans.Get(id)
		switch {
		case !ok:
			m.Problem = "渠道已删除"
		case !slices.Contains(inst.PayTypes(), t):
			m.Channel, m.Row = inst, row
			m.Problem = fmt.Sprintf("渠道「%s」不再开放%s", row.Name, channel.TypeLabel(t))
		default:
			m.Channel, m.Row = inst, row
			if err := inst.Ready(ctx); err != nil {
				m.Problem = err.Error()
			}
		}
		out = append(out, m)
	}
	return out
}

// Find 从 Methods 的结果里找一种支付方式。
func Find(ms []Method, typ string) (Method, bool) {
	i := slices.IndexFunc(ms, func(m Method) bool { return m.Type == typ })
	if i < 0 {
		return Method{}, false
	}
	return ms[i], true
}

// Labels 把这些支付方式的中文名用“或”连起来，例如“支付宝或微信支付”。
func Labels(ms []Method) string {
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = channel.TypeLabel(m.Type)
	}
	return strings.Join(names, "或")
}

// Of 是已经定了渠道的订单换算出的支付方式：typ 为空时用订单的支付方式。渠道已删除时返回 false。
func Of(chans *registry.Registry, o *store.Order, typ string) (Method, bool) {
	inst, row, ok := chans.ByKey(o.Channel)
	if !ok {
		return Method{}, false
	}
	if typ == "" {
		typ = o.PayType
	}
	return Method{Type: typ, Channel: inst, Row: row}, true
}

// Choose 按付款人或调用方选的支付方式定下订单：还没定渠道的订单定渠道（按金额推断到账的渠道当场分配金额尾数），
// 走第三方易支付的接着预下单（已经预下单过的直接返回原结果）。订单已经定了别的方式时返回 order.ErrChosen。
// 这次才定下的订单预下单失败时撤回选择：付款人没拿到付款码，可以换一种方式。
// 返回的预下单结果只有第三方易支付才有。
func Choose(ctx context.Context, orders *order.Service, cf *confirm.Service, o *store.Order, m Method, base string) (*store.UpstreamPayment, error) {
	fresh := o.Channel == ""
	switch {
	case o.Channel == "":
		// 定下来就不能换了，先看金额能不能用这种方式，免得锁在一个付不了的方式上。
		if err := m.CheckAmount(o.Amount, time.Now()); err != nil {
			return nil, err
		}
		chosen, err := orders.Choose(ctx, o.ID, m.Row.Key(), m.Type, m.Confirming())
		if err != nil {
			return nil, err
		}
		*o = *chosen
	case o.Channel != m.Row.Key():
		return nil, order.ErrChosen
	case !m.Confirming() && o.PayType != "" && o.PayType != m.Type:
		// 第三方易支付按预下单记录判断换没换（见 confirm.Service.Prepare）
		return nil, order.ErrChosen
	}
	if !m.Confirming() {
		return nil, nil
	}
	up, err := cf.Prepare(ctx, o, m.Type, base)
	if errors.Is(err, confirm.ErrNotConfirmer) {
		return nil, nil
	}
	if err != nil && fresh {
		if uerr := orders.Unchoose(ctx, o.ID, o.Channel, o.PayType); uerr != nil {
			slog.Error("撤回支付方式失败", "trade_no", o.TradeNo, "err", uerr)
		}
	}
	return up, err
}
