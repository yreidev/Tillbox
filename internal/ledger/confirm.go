package ledger

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"

	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/order"
	"tillbox/internal/store"
)

// Confirm 记下第三方平台确认的一笔付款（确认型渠道的回调或查单），能到账就确认到账。返回是否确认了到账。
//
// 第三方平台单号作为流水号，同一渠道内只处理一次，所以重复回调、回调和查单先后确认都是幂等的。
// 订单待支付或已过期、金额和订单原价一致时自动到账；过期后才确认的另记一条待处理的迟付异常，
// 提醒到商户后台核对。已支付、已关闭、争议中或金额不符的不到账，只记异常。
func (l *Ledger) Confirm(ctx context.Context, orderID int64, p channel.Paid) (bool, error) {
	unlock := l.orders.Lock()
	defer unlock()
	o, err := l.orders.ByID(ctx, orderID)
	if err != nil {
		return false, err
	}
	exists, err := l.db.NewSelect().Model((*store.Transaction)(nil)).
		Where("channel = ?", o.Channel).Where("external_id = ?", p.TradeNo).Exists(ctx)
	if err != nil || exists {
		return false, err
	}
	now := l.Now()
	paidAt := p.PaidAt
	if paidAt.IsZero() || paidAt.After(now) || store.Ms(paidAt)+TimePrecision.Milliseconds() <= o.CreatedAt {
		paidAt = now
	}
	e := channel.Entry{
		ExternalID: p.TradeNo, Direction: store.DirIn, Amount: p.Amount, OccurredAt: paidAt,
		AlipayOrderNo: p.APITradeNo, MerchantOrderNo: o.TradeNo, OtherAccount: p.Buyer,
		Memo: channel.TypeLabel(p.Type), BizDesc: "第三方易支付确认收款", Kind: "upstream", Raw: p.Raw,
	}
	nowMs := store.Ms(now)
	t := &store.Transaction{
		Channel: o.Channel, ExternalID: e.ExternalID, Direction: e.Direction, Amount: e.Amount,
		OccurredAt: store.Ms(e.OccurredAt), AlipayOrderNo: e.AlipayOrderNo, MerchantOrderNo: e.MerchantOrderNo,
		OtherAccount: e.OtherAccount, Memo: e.Memo, BizDesc: e.BizDesc, Kind: e.Kind, Raw: e.Raw,
		Fingerprint: Fingerprint(e), State: store.TxnNew, CreatedAt: nowMs, UpdatedAt: nowMs,
	}
	insert := func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewInsert().Model(t).Exec(ctx)
		return err
	}
	flag := func(kind, detail string) (bool, error) {
		return false, l.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			if err := insert(ctx, tx); err != nil {
				return err
			}
			return l.addAnomaly(ctx, tx, kind, t, o.ID, detail, true)
		})
	}
	amount := epay.FormatCents(p.Amount)
	switch {
	case o.Status == store.OrderPaid:
		return flag(store.AnomalyDuplicate, fmt.Sprintf("订单 %s 已经付过款，第三方平台又确认了一笔 %s（第三方平台单号 %s）。可能是重复付款，需要到第三方平台核对并退回。",
			o.TradeNo, amount, p.TradeNo))
	case p.Amount != o.Amount:
		kind, word := store.AnomalyUnderpaid, "少"
		if p.Amount > o.Amount {
			kind, word = store.AnomalyOverpaid, "多"
		}
		return flag(kind, fmt.Sprintf("第三方平台确认订单 %s 收款 %s，订单金额 %s，%s了 %s，没有自动到账。", o.TradeNo, amount,
			epay.FormatCents(o.Amount), word, epay.FormatCents(abs(p.Amount-o.Amount))))
	case o.Status == store.OrderClosed:
		return flag(store.AnomalyLate, fmt.Sprintf("订单 %s 在 %s 已经关闭，之后第三方平台确认收款 %s（第三方平台单号 %s），没有自动到账。请人工核对后在流水页关联到订单，或到第三方平台退款。",
			o.TradeNo, epay.FormatTime(store.Time(o.EndedAt)), amount, p.TradeNo))
	case o.Status != store.OrderPending && o.Status != store.OrderExpired:
		return flag(store.AnomalyLate, fmt.Sprintf("订单 %s 现在是人工处理状态，之后第三方平台又确认收款 %s（第三方平台单号 %s），没有自动到账，请人工处理。",
			o.TradeNo, amount, p.TradeNo))
	}
	late := order.Effective(o, now) == store.OrderExpired && store.Ms(paidAt) > o.ExpiresAt
	err = l.orders.MarkPaidTx(ctx, o, t, insert, func(ctx context.Context, tx bun.Tx) error {
		if !late {
			return nil
		}
		return l.addAnomaly(ctx, tx, store.AnomalyLate, t, o.ID, fmt.Sprintf(
			"订单 %s 在 %s 已经过期，第三方平台 %s 才确认收款 %s（第三方平台单号 %s），已自动到账并通知商户。"+
				"有的调用方对已失效的订单回 success 却不入账，请到商户后台核对这笔订单，确认入账（或手动补充）后把这条异常标为已处理。",
			o.TradeNo, epay.FormatTime(store.Time(o.ExpiresAt)), epay.FormatTime(paidAt), amount, p.TradeNo), false)
	})
	if err != nil {
		return false, err
	}
	if l.orders.OnPaid != nil {
		l.orders.OnPaid()
	}
	return true, nil
}
