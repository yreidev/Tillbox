package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/store"
)

// LinkVerified is the only automatic exception-recovery path. All evidence is
// checked again under the same lock used by reconciliation and manual changes.
// A model's confidence is never evidence of payment.
func (l *Ledger) LinkVerified(ctx context.Context, txnID, orderID int64, reference string, amount, paidAt int64) error {
	unlock := l.orders.Lock()
	defer unlock()
	var t store.Transaction
	if err := l.db.NewSelect().Model(&t).Where("id = ?", txnID).Scan(ctx); err != nil {
		return err
	}
	o, err := l.orders.ByID(ctx, orderID)
	if err != nil {
		return err
	}
	if reference == "" || (reference != t.AlipayOrderNo && reference != t.ExternalID) || amount != t.Amount || t.Amount != o.PayAmount || t.Direction != store.DirIn || t.Channel != o.Channel || t.OrderID != 0 || (t.State != store.TxnNew && t.State != store.TxnAnomaly) || (o.Status != store.OrderPending && o.Status != store.OrderExpired) {
		return errors.New("流水编号、金额、渠道或订单状态不能通过自动补认校验")
	}
	if paidAt < t.OccurredAt-2*time.Minute.Milliseconds() || paidAt > t.OccurredAt+2*time.Minute.Milliseconds() || t.OccurredAt+TimePrecision.Milliseconds() <= o.CreatedAt || t.OccurredAt > o.ExpiresAt+LateWindow.Milliseconds() {
		return errors.New("付款时间不能通过自动补认校验")
	}
	// Any amount reuse in the possible late-payment window makes ownership ambiguous.
	others, err := l.db.NewSelect().Model((*store.Order)(nil)).Where("channel = ?", o.Channel).
		Where("id <> ?", o.ID).Where("pay_amount = ?", t.Amount).
		Where("created_at < ?", t.OccurredAt+TimePrecision.Milliseconds()).
		Where("expires_at >= ?", t.OccurredAt-LateWindow.Milliseconds()).Exists(ctx)
	if err != nil {
		return err
	}
	if others {
		return errors.New("付款窗口内存在其他同额订单，需要人工确认归属")
	}
	blocked, err := l.db.NewSelect().Model((*store.Anomaly)(nil)).Where("transaction_id = ?", t.ID).
		Where("kind IN (?)", bun.List(store.ManualAnomalies)).Exists(ctx)
	if err != nil {
		return err
	}
	if blocked {
		return errors.New("流水存在冲突、重复付款、歧义、人工撤销或解析不了的记录，需要人工处理")
	}
	now := store.Ms(l.Now())
	err = l.orders.MarkPaid(ctx, o, &t, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model((*store.Anomaly)(nil)).
			Set("status = ?", store.AnomalyResolved).Set("resolved_at = ?", now).
			Set("resolved_by = ?", "AI 自动处理").Set("note = ?", "通过真实流水编号、金额、时间及唯一归属校验，自动补认到账").
			Where("transaction_id = ?", t.ID).Where("status = ?", store.AnomalyOpen).Exec(ctx); err != nil {
			return err
		}
		return store.Audit(ctx, tx, "AI 自动处理", "ledger.verified_link", o.TradeNo, fmt.Sprintf("已校验真实流水 ID=%d，金额和付款时间相符且没有其他同额候选订单", t.ID))
	})
	if err == nil && l.orders.OnPaid != nil {
		l.orders.OnPaid()
	}
	return err
}
