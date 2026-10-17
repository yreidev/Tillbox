// Package ledger 负责流水：入库去重、按金额和时间推断归属、记异常、人工关联和撤销，以及定时查账。
// 规则见 docs/research/collection-principle.md 第 4、5 节。
package ledger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/order"
	"tillbox/internal/store"
)

// LateWindow 是"结束后付款"的判定范围：订单结束后这么久以内到的同额付款。
const LateWindow = 24 * time.Hour

// TimePrecision 是流水时间的精度。支付宝流水时间只到秒，下单后同一秒内付的款，
// 流水时间会早于订单创建时刻（毫秒），比较"付款晚于下单"时要放宽这么多。
const TimePrecision = time.Second

type Ledger struct {
	db     *bun.DB
	orders *order.Service
	// Now 可以在测试里替换。
	Now func() time.Time
}

func New(db *bun.DB, orders *order.Service) *Ledger {
	return &Ledger{db: db, orders: orders, Now: time.Now}
}

// Fingerprint 是流水内容的摘要。同一个流水号再出现时内容变了，就记冲突。
func Fingerprint(e channel.Entry) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%d|%s|%s|%s", e.Direction, e.Amount, e.OccurredAt.Unix(), e.AlipayOrderNo, e.MerchantOrderNo, e.OtherAccount)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Result 是一批流水的处理结果。
type Result struct {
	New       int
	Matched   int
	Anomalies int
	Conflicts int
}

// Ingest 入库一批流水并对账。同一个流水号只入库一次；内容变了记冲突，不覆盖原记录。
// 入库和对账分两步：先把整批流水存下来，再对账所有还没处理的收入流水，
// 所以上一轮入库后对账出错留下的流水，这一轮会接着对账。
func (l *Ledger) Ingest(ctx context.Context, channelKey string, entries []channel.Entry) (Result, error) {
	var res Result
	unlock := l.orders.Lock()
	defer unlock()
	for _, e := range entries {
		var t store.Transaction
		err := l.db.NewSelect().Model(&t).
			Where("channel = ?", channelKey).Where("external_id = ?", e.ExternalID).Limit(1).Scan(ctx)
		fp := Fingerprint(e)
		if err == nil {
			if t.Fingerprint != fp {
				added, err := l.recordConflict(ctx, &t, e, fp)
				if err != nil {
					return res, err
				}
				if added {
					res.Conflicts++
				}
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return res, err
		}
		now := store.Ms(l.Now())
		t = store.Transaction{
			Channel: channelKey, ExternalID: e.ExternalID, Direction: e.Direction, Amount: e.Amount,
			OccurredAt: store.Ms(e.OccurredAt), AlipayOrderNo: e.AlipayOrderNo, MerchantOrderNo: e.MerchantOrderNo,
			OtherAccount: e.OtherAccount, Memo: e.Memo, BizDesc: e.BizDesc, Kind: e.Kind, Raw: e.Raw,
			Fingerprint: fp, State: store.TxnNew, CreatedAt: now, UpdatedAt: now,
		}
		if t.Direction != store.DirIn {
			t.State = store.TxnIgnored
		}
		if _, err := l.db.NewInsert().Model(&t).Exec(ctx); err != nil {
			return res, err
		}
		res.New++
	}
	err := l.matchPending(ctx, channelKey, &res)
	return res, err
}

// MatchPending 对账还没处理的收入流水。查账每轮开头调用一次，不依赖这一轮能不能拉到流水。
func (l *Ledger) MatchPending(ctx context.Context, channelKey string) (Result, error) {
	var res Result
	unlock := l.orders.Lock()
	defer unlock()
	err := l.matchPending(ctx, channelKey, &res)
	return res, err
}

// matchPending 按付款时间先后对账还没处理的收入流水。调用方已拿住订单锁。
func (l *Ledger) matchPending(ctx context.Context, channelKey string, res *Result) error {
	var pending []store.Transaction
	err := l.db.NewSelect().Model(&pending).
		Where("channel = ?", channelKey).Where("state = ?", store.TxnNew).Where("direction = ?", store.DirIn).
		Order("occurred_at ASC", "id ASC").Scan(ctx)
	if err != nil {
		return err
	}
	paid := false
	defer func() {
		if paid && l.orders.OnPaid != nil {
			l.orders.OnPaid()
		}
	}()
	for i := range pending {
		matched, err := l.match(ctx, &pending[i])
		if err != nil {
			return err
		}
		if matched {
			res.Matched++
			paid = true
		} else if pending[i].State == store.TxnAnomaly {
			res.Anomalies++
		}
	}
	return nil
}

func (l *Ledger) recordConflict(ctx context.Context, t *store.Transaction, e channel.Entry, fp string) (bool, error) {
	marker := "fingerprint=" + fp
	exists, err := l.db.NewSelect().Model((*store.Anomaly)(nil)).
		Where("transaction_id = ?", t.ID).Where("kind = ?", store.AnomalyConflict).
		Where("detail LIKE ?", "%"+marker+"%").Exists(ctx)
	if err != nil || exists {
		return false, err
	}
	detail := fmt.Sprintf("同一流水号再次出现但内容变了，没有覆盖原记录。新内容：方向 %s，金额 %s，时间 %s，对方 %s，支付宝订单号 %s。%s\n新原文：%s",
		e.Direction, epay.FormatCents(e.Amount), epay.FormatTime(e.OccurredAt), e.OtherAccount, e.AlipayOrderNo, marker, e.Raw)
	return true, l.addAnomaly(ctx, l.db, store.AnomalyConflict, t, t.OrderID, detail, false)
}

func (l *Ledger) addAnomaly(ctx context.Context, db bun.IDB, kind string, t *store.Transaction, orderID int64, detail string, markTxn bool) error {
	now := store.Ms(l.Now())
	a := &store.Anomaly{
		Kind: kind, TransactionID: t.ID, OrderID: orderID, Detail: detail,
		Status: store.AnomalyOpen, CreatedAt: now,
	}
	if _, err := db.NewInsert().Model(a).Exec(ctx); err != nil {
		return err
	}
	if markTxn {
		t.State, t.UpdatedAt = store.TxnAnomaly, now
		_, err := db.NewUpdate().Model(t).Column("state", "updated_at").WherePK().Exec(ctx)
		return err
	}
	return nil
}

// flag 给一笔流水记异常并把它标成"有异常"，两步在同一个事务里，中途出错不会留下半截。
func (l *Ledger) flag(ctx context.Context, kind string, t *store.Transaction, orderID int64, detail string) error {
	return l.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return l.addAnomaly(ctx, tx, kind, t, orderID, detail, true)
	})
}

// windowEnd 是订单接受付款的截止时刻：关闭的订单用关闭时刻，其他用原定到期时刻。
func windowEnd(o *store.Order) int64 {
	if o.Status == store.OrderClosed {
		return o.EndedAt
	}
	return o.ExpiresAt
}

func unpaid(o *store.Order) bool {
	switch o.Status {
	case store.OrderPending, store.OrderExpired, store.OrderClosed:
		return true
	}
	return false
}

func tradeNos(os []store.Order) string {
	nos := make([]string, len(os))
	for i, o := range os {
		nos[i] = o.TradeNo
	}
	return strings.Join(nos, "、")
}

// match 对一笔收入流水做归属推断。调用方已拿住订单锁。返回是否自动确认了到账（没确认的都记了异常或标成不参与）。
// "付款晚于下单"按 TimePrecision 放宽：流水时间 + 精度 > 创建时刻。
func (l *Ledger) match(ctx context.Context, t *store.Transaction) (bool, error) {
	at := t.OccurredAt
	var cands []store.Order
	err := l.db.NewSelect().Model(&cands).
		Where("channel = ?", t.Channel).
		Where("pay_amount = ?", t.Amount).
		Where("created_at < ?", at+TimePrecision.Milliseconds()).
		Where("released_at > ?", at).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.WhereGroup(" OR ", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("status <> ?", store.OrderClosed).Where("expires_at > ?", at)
			}).WhereGroup(" OR ", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("status = ?", store.OrderClosed).Where("ended_at > ?", at)
			})
		}).
		Order("created_at ASC").Scan(ctx)
	if err != nil {
		return false, err
	}
	amount := epay.FormatCents(t.Amount)
	switch {
	case len(cands) == 1 && unpaid(&cands[0]):
		o := cands[0]
		err := l.orders.MarkPaid(ctx, &o, t, nil)
		return err == nil, err
	case len(cands) > 1:
		return false, l.flag(ctx, store.AnomalyAmbiguous, t, 0,
			fmt.Sprintf("金额 %s 同时对上 %d 个订单：%s。请人工确认属于哪一单。", amount, len(cands), tradeNos(cands)))
	case len(cands) == 1:
		return false, l.flag(ctx, store.AnomalyDuplicate, t, cands[0].ID,
			fmt.Sprintf("订单 %s 已经付过款，又收到一笔 %s。可能是重复付款，需要人工退回。", cands[0].TradeNo, amount))
	}

	// 没有候选：依次看是不是结束后才付、是不是金额付错了。
	var late []store.Order
	err = l.db.NewSelect().Model(&late).
		Where("channel = ?", t.Channel).
		Where("pay_amount = ?", t.Amount).
		Where("created_at < ?", at+TimePrecision.Milliseconds()).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.WhereGroup(" OR ", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("status <> ?", store.OrderClosed).Where("expires_at <= ?", at).Where("expires_at > ?", at-LateWindow.Milliseconds())
			}).WhereGroup(" OR ", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("status = ?", store.OrderClosed).Where("ended_at <= ?", at).Where("ended_at > ?", at-LateWindow.Milliseconds())
			})
		}).
		Order("created_at DESC").Limit(1).Scan(ctx)
	if err != nil {
		return false, err
	}
	if len(late) == 1 {
		o := late[0]
		return false, l.flag(ctx, store.AnomalyLate, t, o.ID,
			fmt.Sprintf("订单 %s 在 %s 已经结束，之后才收到 %s。没有自动补认，请人工处理。", o.TradeNo, epay.FormatTime(store.Time(windowEnd(&o))), amount))
	}

	var open []store.Order
	err = l.db.NewSelect().Model(&open).
		Where("channel = ?", t.Channel).
		Where("status IN (?)", bun.List([]string{store.OrderPending, store.OrderExpired, store.OrderClosed})).
		Where("created_at < ?", at+TimePrecision.Milliseconds()).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.WhereGroup(" OR ", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("status <> ?", store.OrderClosed).Where("expires_at > ?", at)
			}).WhereGroup(" OR ", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("status = ?", store.OrderClosed).Where("ended_at > ?", at)
			})
		}).
		Limit(2).Scan(ctx)
	if err != nil {
		return false, err
	}
	if len(open) == 1 {
		o := open[0]
		kind, word := store.AnomalyUnderpaid, "少"
		if t.Amount > o.PayAmount {
			kind, word = store.AnomalyOverpaid, "多"
		}
		return false, l.flag(ctx, kind, t, o.ID,
			fmt.Sprintf("这段时间只有订单 %s 在等付款，应付 %s，实收 %s，%s付了 %s。", o.TradeNo,
				epay.FormatCents(o.PayAmount), amount, word, epay.FormatCents(abs(t.Amount-o.PayAmount))))
	}
	// 比这个渠道的第一个订单还早的收入不可能是订单的付款（例如第一次查账补回来的历史流水），不记异常。
	earlier, err := l.db.NewSelect().Model((*store.Order)(nil)).
		Where("channel = ?", t.Channel).Where("created_at < ?", at+TimePrecision.Milliseconds()).Exists(ctx)
	if err != nil {
		return false, err
	}
	if !earlier {
		t.State, t.UpdatedAt = store.TxnIgnored, store.Ms(l.Now())
		_, err := l.db.NewUpdate().Model(t).Column("state", "updated_at").WherePK().Exec(ctx)
		return false, err
	}
	return false, l.flag(ctx, store.AnomalyUnmatched, t, 0,
		fmt.Sprintf("收到 %s，没有对得上的订单。", amount))
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// Link 人工把一笔收入流水关联到一个订单，确认到账。
func (l *Ledger) Link(ctx context.Context, txnID, orderID int64, actor, note string) error {
	unlock := l.orders.Lock()
	defer unlock()
	var t store.Transaction
	if err := l.db.NewSelect().Model(&t).Where("id = ?", txnID).Scan(ctx); err != nil {
		return fmt.Errorf("流水不存在：%w", err)
	}
	if t.Direction != store.DirIn {
		return errors.New("只能关联收入流水")
	}
	if t.State == store.TxnMatched {
		return errors.New("这笔流水已经关联了订单，要改先撤销原来的关联")
	}
	o, err := l.orders.ByID(ctx, orderID)
	if err != nil {
		return err
	}
	if o.Status == store.OrderPaid {
		return errors.New("这个订单已经是已支付状态")
	}
	if o.Channel != t.Channel {
		return errors.New("订单和流水不是同一个收款渠道")
	}
	now := store.Ms(l.Now())
	err = l.orders.MarkPaid(ctx, o, &t, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewUpdate().Model((*store.Anomaly)(nil)).
			Set("status = ?", store.AnomalyResolved).Set("resolved_at = ?", now).
			Set("resolved_by = ?", actor).Set("note = ?", "已人工关联到订单 "+o.TradeNo+"。"+note).
			Where("transaction_id = ?", t.ID).Where("status = ?", store.AnomalyOpen).Exec(ctx)
		if err != nil {
			return err
		}
		return store.Audit(ctx, tx, actor, "ledger.link", o.TradeNo,
			fmt.Sprintf("流水 %s（%s）关联到订单 %s。理由：%s", t.ExternalID, epay.FormatCents(t.Amount), o.TradeNo, note))
	})
	if err == nil && l.orders.OnPaid != nil {
		l.orders.OnPaid()
	}
	return err
}

// Unlink 撤销订单的到账关联：订单进入争议状态，流水回到待处理，并记一条异常。
func (l *Ledger) Unlink(ctx context.Context, orderID int64, actor, note string) error {
	unlock := l.orders.Lock()
	defer unlock()
	o, err := l.orders.ByID(ctx, orderID)
	if err != nil {
		return err
	}
	if o.Status != store.OrderPaid {
		return errors.New("只有已支付的订单才能撤销到账关联")
	}
	now := store.Ms(l.Now())
	return l.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var t store.Transaction
		hasTxn := o.TxnID != 0
		if hasTxn {
			if err := tx.NewSelect().Model(&t).Where("id = ?", o.TxnID).Scan(ctx); err != nil {
				return err
			}
		}
		o.Status, o.PaidAt, o.PaidAmount, o.TxnID, o.UpdatedAt = store.OrderDisputed, 0, 0, 0, now
		_, err := tx.NewUpdate().Model(o).Column("status", "paid_at", "paid_amount", "txn_id", "updated_at").WherePK().Exec(ctx)
		if err != nil {
			return err
		}
		if hasTxn {
			t.OrderID = 0
			if err := l.addAnomaly(ctx, tx, store.AnomalyUnlinked, &t, o.ID,
				fmt.Sprintf("人工撤销了订单 %s 的到账关联（%s 操作）。理由：%s", o.TradeNo, actor, note), true); err != nil {
				return err
			}
			if _, err := tx.NewUpdate().Model(&t).Column("order_id").WherePK().Exec(ctx); err != nil {
				return err
			}
		}
		_, err = tx.NewUpdate().Model((*store.Notification)(nil)).
			Set("status = ?", store.NotifyDead).Set("last_error = ?", "到账关联已撤销，停止通知").Set("updated_at = ?", now).
			Where("order_id = ?", o.ID).Where("status = ?", store.NotifyPending).Exec(ctx)
		if err != nil {
			return err
		}
		return store.Audit(ctx, tx, actor, "ledger.unlink", o.TradeNo, "撤销到账关联。理由："+note)
	})
}

// Resolve 把一条异常标成已处理（例如已经线下退款）。
func (l *Ledger) Resolve(ctx context.Context, anomalyID int64, actor, note string) error {
	now := store.Ms(l.Now())
	return l.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewUpdate().Model((*store.Anomaly)(nil)).
			Set("status = ?", store.AnomalyResolved).Set("resolved_at = ?", now).
			Set("resolved_by = ?", actor).Set("note = ?", note).
			Where("id = ?", anomalyID).Where("status = ?", store.AnomalyOpen).Exec(ctx)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("异常不存在或已经处理过了")
		}
		return store.Audit(ctx, tx, actor, "anomaly.resolve", fmt.Sprint(anomalyID), note)
	})
}
