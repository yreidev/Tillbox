// Package payroute 按商户的收款方式决定订单走哪个渠道：下单、收银台选支付方式和后台支付测试都用它。
// 商户每种支付方式选一个或几个渠道，选了几个时新订单按权重在能收款的渠道里随机分；
// 下单时没给支付方式、商户又开了不止一种时，订单先不定渠道，由付款人在收银台选，选定后不能再换（只能取消订单重新下单）。
package payroute

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/channel/registry"
	"tillbox/internal/confirm"
	"tillbox/internal/order"
	"tillbox/internal/store"
)

// Route 是一种支付方式选的一个渠道。
type Route struct {
	Type    string
	Channel channel.Instance // 渠道已删除时为 nil
	Row     store.Channel
	Weight  int
	// Problem 是这个渠道现在收不了这种方式的原因（渠道删了、停用、没配好、不再开放这种方式），能收款时为空。
	Problem string
}

// Ready 判断这个渠道现在能不能收这种方式的款。
func (r Route) Ready() bool { return r.Problem == "" }

// Confirming 判断是不是逐单确认到账的渠道（第三方易支付）。这种渠道按原价付款，不加金额尾数。
func (r Route) Confirming() bool {
	_, ok := r.Channel.(channel.Confirmer)
	return ok
}

// CheckAmount 看金额有没有超出这个渠道这种方式已知的单笔限额，超出时返回 *channel.AmountError。
func (r Route) CheckAmount(amount int64, now time.Time) error {
	l, ok := r.Channel.(channel.Limiter)
	if !ok {
		return nil
	}
	return l.CheckAmount(r.Type, amount, now)
}

// Method 是商户开放的一种支付方式和它选的渠道（按渠道编号升序）。
type Method struct {
	Type   string
	Routes []Route
}

// Ready 判断这种方式现在能不能收款：选的渠道里至少有一个能收。
func (m Method) Ready() bool { return slices.ContainsFunc(m.Routes, Route.Ready) }

// Problem 列出选的渠道各自收不了款的原因，都能收款时为空。
func (m Method) Problem() string {
	var out []string
	for _, r := range m.Routes {
		if r.Problem != "" {
			out = append(out, r.Problem)
		}
	}
	return strings.Join(out, "；")
}

// CheckAmount 看这种方式能不能收这个金额：能收款的渠道都超出已知的单笔限额时返回第一个的 *channel.AmountError。
func (m Method) CheckAmount(amount int64, now time.Time) error {
	var first error
	for _, r := range m.Routes {
		if !r.Ready() {
			continue
		}
		err := r.CheckAmount(amount, now)
		if err == nil {
			return nil
		}
		if first == nil {
			first = err
		}
	}
	return first
}

// ErrUnavailable 是这种方式选的渠道都收不了款。
var ErrUnavailable = errors.New("支付方式选的渠道都收不了款")

// intN 是 Pick 用的随机数（测试替换）。
var intN = rand.IntN

// Pick 给新订单挑一个渠道：在能收款、金额没超出已知单笔限额的渠道里按权重随机挑。
// 都收不了款时返回 ErrUnavailable；能收款的都超出限额时返回 CheckAmount 的错误。
func (m Method) Pick(amount int64, now time.Time) (Route, error) {
	var ok []Route
	total := 0
	for _, r := range m.Routes {
		if r.Ready() && r.CheckAmount(amount, now) == nil {
			ok = append(ok, r)
			total += r.Weight
		}
	}
	if len(ok) == 0 {
		if !m.Ready() {
			return Route{}, ErrUnavailable
		}
		return Route{}, m.CheckAmount(amount, now)
	}
	n := intN(total)
	for _, r := range ok {
		if n < r.Weight {
			return r, nil
		}
		n -= r.Weight
	}
	panic("unreachable")
}

// Methods 按支付方式的固定顺序列出商户开放的方式，以及各自选的渠道现在能不能收款。
func Methods(ctx context.Context, chans *registry.Registry, routes store.Routes) []Method {
	var out []Method
	for _, t := range channel.TypeOrder {
		if len(routes[t]) == 0 {
			continue
		}
		m := Method{Type: t}
		for _, sel := range routes[t] {
			r := Route{Type: t, Weight: sel.Weight}
			inst, row, ok := chans.Get(sel.ChannelID)
			switch {
			case !ok:
				r.Row.ID = sel.ChannelID
				r.Problem = "渠道已删除"
			case !slices.Contains(inst.PayTypes(), t):
				r.Channel, r.Row = inst, row
				r.Problem = fmt.Sprintf("渠道「%s」不再开放%s", row.Name, channel.TypeLabel(t))
			default:
				r.Channel, r.Row = inst, row
				if err := inst.Ready(ctx); err != nil {
					r.Problem = err.Error()
				}
			}
			m.Routes = append(m.Routes, r)
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

// Of 是已经定了渠道的订单走的渠道：typ 为空时用订单的支付方式。渠道已删除时返回 false。
func Of(chans *registry.Registry, o *store.Order, typ string) (Route, bool) {
	inst, row, ok := chans.ByKey(o.Channel)
	if !ok {
		return Route{}, false
	}
	if typ == "" {
		typ = o.PayType
	}
	return Route{Type: typ, Channel: inst, Row: row}, true
}

// Choose 按付款人或调用方选的支付方式和挑好的渠道定下订单：还没定渠道的订单定渠道（按金额推断到账的渠道当场分配金额尾数），
// 走第三方易支付的接着预下单（已经预下单过的直接返回原结果）。订单已经定了别的方式时返回 order.ErrChosen。
// 这次才定下的订单预下单失败时撤回选择：付款人没拿到付款码，可以换一种方式。
// 返回订单实际走的渠道（同时有别的请求给订单选了同一种方式时用那边挑的渠道），预下单结果只有第三方易支付才有。
func Choose(ctx context.Context, chans *registry.Registry, orders *order.Service, cf *confirm.Service, o *store.Order, r Route, base string) (Route, *store.UpstreamPayment, error) {
	fresh := o.Channel == ""
	switch {
	case o.Channel == "":
		// 定下来就不能换了，先看金额能不能用这种方式，免得锁在一个付不了的方式上。
		if err := r.CheckAmount(o.Amount, time.Now()); err != nil {
			return r, nil, err
		}
		chosen, err := orders.Choose(ctx, o.ID, r.Row.Key(), r.Type, r.Confirming())
		if err != nil {
			return r, nil, err
		}
		*o = *chosen
		if o.Channel != r.Row.Key() {
			var ok bool
			if r, ok = Of(chans, o, r.Type); !ok {
				return r, nil, fmt.Errorf("订单 %s 的渠道 %s 不存在", o.TradeNo, o.Channel)
			}
			fresh = false
		}
	case o.Channel != r.Row.Key():
		return r, nil, order.ErrChosen
	case !r.Confirming() && o.PayType != "" && o.PayType != r.Type:
		// 第三方易支付按预下单记录判断换没换（见 confirm.Service.Prepare）
		return r, nil, order.ErrChosen
	}
	if !r.Confirming() {
		return r, nil, nil
	}
	up, err := cf.Prepare(ctx, o, r.Type, base)
	if errors.Is(err, confirm.ErrNotConfirmer) {
		return r, nil, nil
	}
	if err != nil && fresh {
		if uerr := orders.Unchoose(ctx, o.ID, o.Channel, o.PayType); uerr != nil {
			slog.Error("撤回支付方式失败", "trade_no", o.TradeNo, "err", uerr)
		}
	}
	return r, up, err
}
