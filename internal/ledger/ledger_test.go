package ledger_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/channel"
	"tillbox/internal/ledger"
	"tillbox/internal/order"
	"tillbox/internal/store"
)

type fixture struct {
	*apptest.Env
	m   *apptest.Merchant
	seq int
}

func newFixture(t *testing.T) *fixture {
	e := apptest.New(t)
	return &fixture{Env: e, m: e.NewMerchant(nil)}
}

func (f *fixture) order(no string, cents int64) *store.Order {
	f.T.Helper()
	o, _, err := f.App.Orders.Create(f.Ctx, order.CreateInput{
		MerchantID: f.m.ID, OutTradeNo: no, Version: order.V1, Channel: f.m.Channel, PayType: "alipay", Name: "x",
		Money: fmt.Sprintf("%d.%02d", cents/100, cents%100), Amount: cents, NotifyURL: f.Hook.URL, ClientIP: "127.0.0.1",
	})
	if err != nil {
		f.T.Fatal(err)
	}
	return o
}

// shift 把订单的时间整体往前拨 d（模拟订单是 d 之前建的）。
func (f *fixture) shift(o *store.Order, d time.Duration) {
	f.T.Helper()
	ms := d.Milliseconds()
	_, err := f.DB().NewUpdate().Model((*store.Order)(nil)).
		Set("created_at = created_at - ?", ms).Set("expires_at = expires_at - ?", ms).Set("released_at = released_at - ?", ms).
		Where("id = ?", o.ID).Exec(f.Ctx)
	if err != nil {
		f.T.Fatal(err)
	}
}

func (f *fixture) entry(cents int64, at time.Time) channel.Entry {
	f.seq++
	return channel.Entry{
		ExternalID: fmt.Sprintf("L%06d", f.seq), Direction: store.DirIn, Amount: cents, OccurredAt: at,
		AlipayOrderNo: fmt.Sprintf("2026101522%018d", f.seq), OtherAccount: "付款人", Kind: "在线支付", Raw: "{}",
	}
}

func (f *fixture) ingest(es ...channel.Entry) {
	f.T.Helper()
	if _, err := f.App.Ledger.Ingest(f.Ctx, f.ChannelKey(), es); err != nil {
		f.T.Fatal(err)
	}
}

func (f *fixture) reload(o *store.Order) *store.Order {
	f.T.Helper()
	got, err := f.App.Orders.ByID(f.Ctx, o.ID)
	if err != nil {
		f.T.Fatal(err)
	}
	return got
}

func (f *fixture) anomalies() []store.Anomaly {
	f.T.Helper()
	var as []store.Anomaly
	if err := f.DB().NewSelect().Model(&as).Order("id ASC").Scan(f.Ctx); err != nil {
		f.T.Fatal(err)
	}
	return as
}

func (f *fixture) wantAnomaly(kind string, orderID int64) store.Anomaly {
	f.T.Helper()
	as := f.anomalies()
	if len(as) == 0 {
		f.T.Fatalf("应有一条 %s 异常，实际没有", kind)
	}
	a := as[len(as)-1]
	if a.Kind != kind || a.OrderID != orderID || a.Status != store.AnomalyOpen {
		f.T.Fatalf("最新异常应是 %s（订单 %d），实际 %s（订单 %d）：%s", kind, orderID, a.Kind, a.OrderID, a.Detail)
	}
	return a
}

func TestMatchUnique(t *testing.T) {
	f := newFixture(t)
	o := f.order("A", 1000)
	f.ingest(f.entry(o.PayAmount, time.Now()))
	got := f.reload(o)
	if got.Status != store.OrderPaid || got.PaidAmount != o.PayAmount || got.TxnID == 0 || got.Buyer != "付款人" || got.APITradeNo == "" {
		t.Fatalf("应确认到账：%+v", got)
	}
	var n store.Notification
	if err := f.DB().NewSelect().Model(&n).Where("order_id = ?", o.ID).Scan(f.Ctx); err != nil || n.Status != store.NotifyPending {
		t.Errorf("应排入通知：%+v %v", n, err)
	}
	if len(f.anomalies()) != 0 {
		t.Error("不该有异常")
	}
}

func TestMatchRules(t *testing.T) {
	t.Run("付款早于下单不算", func(t *testing.T) {
		f := newFixture(t)
		f.shift(f.order("X", 500), time.Hour) // 更早的一单，早就结束了
		o := f.order("A", 1000)
		f.ingest(f.entry(o.PayAmount, time.Now().Add(-time.Minute)))
		if f.reload(o).Status != store.OrderPending {
			t.Error("下单前的付款不该确认")
		}
		f.wantAnomaly(store.AnomalyUnmatched, 0)
	})

	t.Run("流水时间只到秒，同一秒内付的款也算", func(t *testing.T) {
		f := newFixture(t)
		o := f.order("A", 1000)
		created := store.Time(o.CreatedAt)
		if _, err := f.DB().NewUpdate().Model((*store.Order)(nil)).Set("created_at = ?", store.Ms(created.Truncate(time.Second))+999).Where("id = ?", o.ID).Exec(f.Ctx); err != nil {
			t.Fatal(err)
		}
		f.ingest(f.entry(o.PayAmount, created.Truncate(time.Second)))
		if got := f.reload(o); got.Status != store.OrderPaid {
			t.Errorf("同一秒内的付款应确认：%s", got.Status)
		}
	})

	t.Run("过期后才查到、但付款在有效期内", func(t *testing.T) {
		f := newFixture(t)
		o := f.order("A", 1000)
		f.shift(o, 20*time.Minute) // 建于 20 分钟前，5 分钟有效，已过期
		if _, err := f.App.Orders.ExpireDue(f.Ctx); err != nil {
			t.Fatal(err)
		}
		f.ingest(f.entry(o.PayAmount, time.Now().Add(-18*time.Minute)))
		if got := f.reload(o); got.Status != store.OrderPaid {
			t.Errorf("有效期内的付款应确认：%s", got.Status)
		}
	})

	t.Run("关单前付的款仍确认", func(t *testing.T) {
		f := newFixture(t)
		o := f.order("A", 1000)
		payAt := time.Now()
		time.Sleep(5 * time.Millisecond)
		if _, err := f.App.Orders.Close(f.Ctx, o.ID); err != nil {
			t.Fatal(err)
		}
		f.ingest(f.entry(o.PayAmount, payAt))
		if got := f.reload(o); got.Status != store.OrderPaid {
			t.Errorf("关单前的付款应确认：%s", got.Status)
		}
	})

	t.Run("多个候选", func(t *testing.T) {
		f := newFixture(t)
		a := f.order("A", 1000)
		b := f.order("B", 1000)
		// 人为让两单应付金额相同（正常分配不会发生）。
		if _, err := f.DB().NewUpdate().Model((*store.Order)(nil)).Set("pay_amount = ?", a.PayAmount).Where("id = ?", b.ID).Exec(f.Ctx); err != nil {
			t.Fatal(err)
		}
		f.ingest(f.entry(a.PayAmount, time.Now()))
		an := f.wantAnomaly(store.AnomalyAmbiguous, 0)
		if !strings.Contains(an.Detail, a.TradeNo) || !strings.Contains(an.Detail, b.TradeNo) {
			t.Errorf("异常说明应列出两个订单：%s", an.Detail)
		}
		if f.reload(a).Status != store.OrderPending || f.reload(b).Status != store.OrderPending {
			t.Error("有歧义时不能自动确认")
		}
	})

	t.Run("重复付款", func(t *testing.T) {
		f := newFixture(t)
		o := f.order("A", 1000)
		f.ingest(f.entry(o.PayAmount, time.Now()))
		f.ingest(f.entry(o.PayAmount, time.Now()))
		f.wantAnomaly(store.AnomalyDuplicate, o.ID)
	})

	t.Run("结束后才付", func(t *testing.T) {
		f := newFixture(t)
		o := f.order("A", 1000)
		f.shift(o, 20*time.Minute)
		f.ingest(f.entry(o.PayAmount, time.Now()))
		f.wantAnomaly(store.AnomalyLate, o.ID)
		if f.reload(o).Status == store.OrderPaid {
			t.Error("结束后的付款不能自动补认")
		}
	})

	t.Run("关单后才付", func(t *testing.T) {
		f := newFixture(t)
		o := f.order("A", 1000)
		if _, err := f.App.Orders.Close(f.Ctx, o.ID); err != nil {
			t.Fatal(err)
		}
		f.ingest(f.entry(o.PayAmount, time.Now().Add(time.Second)))
		f.wantAnomaly(store.AnomalyLate, o.ID)
	})

	t.Run("少付和多付", func(t *testing.T) {
		f := newFixture(t)
		o := f.order("A", 1000)
		f.ingest(f.entry(1000, time.Now()))
		an := f.wantAnomaly(store.AnomalyUnderpaid, o.ID)
		if !strings.Contains(an.Detail, "少付了 0.01") {
			t.Errorf("说明：%s", an.Detail)
		}
		f.ingest(f.entry(1100, time.Now()))
		f.wantAnomaly(store.AnomalyOverpaid, o.ID)
	})

	t.Run("多个待付订单时金额不对", func(t *testing.T) {
		f := newFixture(t)
		f.order("A", 1000)
		f.order("B", 2000)
		f.ingest(f.entry(1500, time.Now()))
		f.wantAnomaly(store.AnomalyUnmatched, 0)
	})

	t.Run("没有对得上的订单", func(t *testing.T) {
		f := newFixture(t)
		f.shift(f.order("A", 1000), time.Hour)
		f.ingest(f.entry(333, time.Now()))
		f.wantAnomaly(store.AnomalyUnmatched, 0)
	})

	t.Run("比第一个订单还早的收入不参与", func(t *testing.T) {
		f := newFixture(t)
		f.ingest(f.entry(333, time.Now().Add(-time.Hour)))
		f.order("A", 1000)
		f.ingest(f.entry(444, time.Now().Add(-time.Minute)))
		if as := f.anomalies(); len(as) != 0 {
			t.Errorf("不该记异常：%+v", as)
		}
		var txs []store.Transaction
		if err := f.DB().NewSelect().Model(&txs).Scan(f.Ctx); err != nil {
			t.Fatal(err)
		}
		for _, tx := range txs {
			if tx.State != store.TxnIgnored {
				t.Errorf("流水 %s 应标为不参与：%s", tx.ExternalID, tx.State)
			}
		}
	})

	t.Run("支出不对账", func(t *testing.T) {
		f := newFixture(t)
		o := f.order("A", 1000)
		e := f.entry(o.PayAmount, time.Now())
		e.Direction = store.DirOut
		f.ingest(e)
		if f.reload(o).Status != store.OrderPending || len(f.anomalies()) != 0 {
			t.Error("支出流水不该参与对账")
		}
		var tx store.Transaction
		if err := f.DB().NewSelect().Model(&tx).Limit(1).Scan(f.Ctx); err != nil {
			t.Fatal(err)
		}
		if tx.State != store.TxnIgnored {
			t.Errorf("支出流水应标为忽略：%s", tx.State)
		}
	})
}

// 入库后对账中途出错（数据库忙、进程被杀）留下的流水，下一轮要接着对账，不能因为流水号已存在就跳过。
func TestMatchLeftover(t *testing.T) {
	f := newFixture(t)
	o := f.order("A", 1000)
	e := f.entry(o.PayAmount, time.Now())
	now := store.Ms(time.Now())
	left := &store.Transaction{
		Channel: f.ChannelKey(), ExternalID: e.ExternalID, Direction: e.Direction, Amount: e.Amount, OccurredAt: store.Ms(e.OccurredAt),
		AlipayOrderNo: e.AlipayOrderNo, OtherAccount: e.OtherAccount, Kind: e.Kind, Raw: e.Raw,
		Fingerprint: ledger.Fingerprint(e), State: store.TxnNew, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := f.DB().NewInsert().Model(left).Exec(f.Ctx); err != nil {
		t.Fatal(err)
	}
	// 这一轮拉不到流水也要对账。
	res, err := f.App.Ledger.MatchPending(f.Ctx, f.ChannelKey())
	if err != nil || res.Matched != 1 {
		t.Fatalf("应对上 1 笔：%+v %v", res, err)
	}
	if got := f.reload(o); got.Status != store.OrderPaid || got.TxnID != left.ID {
		t.Fatalf("遗留流水应确认到账：%+v", got)
	}
	// 同一流水再拉到时不会重复处理。
	f.ingest(e)
	if len(f.anomalies()) != 0 {
		t.Error("不该有异常")
	}
}

func TestDedupAndConflict(t *testing.T) {
	f := newFixture(t)
	o := f.order("A", 1000)
	e := f.entry(o.PayAmount, time.Now())
	f.ingest(e)
	f.ingest(e, e)
	n, _ := f.DB().NewSelect().Model((*store.Transaction)(nil)).Count(f.Ctx)
	if n != 1 {
		t.Errorf("同一流水号只该入库一次，实际 %d", n)
	}
	changed := e
	changed.Amount++
	f.ingest(changed)
	f.ingest(changed)
	as := f.anomalies()
	if len(as) != 1 || as[0].Kind != store.AnomalyConflict {
		t.Fatalf("内容变了应记一次冲突：%+v", as)
	}
	var tx store.Transaction
	if err := f.DB().NewSelect().Model(&tx).Limit(1).Scan(f.Ctx); err != nil {
		t.Fatal(err)
	}
	if tx.Amount != e.Amount || tx.State != store.TxnMatched {
		t.Error("冲突时不能覆盖原记录")
	}
}

func TestInvalidEntry(t *testing.T) {
	f := newFixture(t)
	o := f.order("A", 1000)
	good := f.entry(o.PayAmount, time.Now())
	bad := channel.Entry{
		ExternalID: good.ExternalID, Direction: store.DirIn, AlipayOrderNo: good.AlipayOrderNo,
		Raw: `{"trans_dt":"bad"}`, Invalid: "时间格式不对",
	}
	other := f.order("B", 2000)
	f.ingest(bad, f.entry(other.PayAmount, time.Now()))
	if f.reload(other).Status != store.OrderPaid {
		t.Fatal("同一批里能解析的流水应照常对账")
	}
	a := f.wantAnomaly(store.AnomalyInvalid, 0)
	var tx store.Transaction
	if err := f.DB().NewSelect().Model(&tx).Where("external_id = ?", bad.ExternalID).Scan(f.Ctx); err != nil {
		t.Fatal(err)
	}
	if tx.ID != a.TransactionID || tx.State != store.TxnAnomaly || tx.Raw != bad.Raw || !ledger.Invalid(&tx) {
		t.Fatalf("解析不了的流水应连原文入库并标成异常：%+v", tx)
	}
	if d := time.Since(store.Time(tx.OccurredAt)); d < 0 || d > time.Minute {
		t.Errorf("时间读不出来时应按入库时刻记，实际 %s", store.Time(tx.OccurredAt))
	}
	if err := f.App.Ledger.Link(f.Ctx, tx.ID, o.ID, "admin", ""); err == nil || !strings.Contains(err.Error(), "解析不了") {
		t.Errorf("解析不了的流水不能关联订单：%v", err)
	}

	// 再拉到同样解析不了的内容不重复记。
	f.ingest(bad)
	if n := len(f.anomalies()); n != 1 {
		t.Fatalf("同一条坏流水只记一次异常，实际 %d", n)
	}
	if f.reload(o).Status == store.OrderPaid {
		t.Fatal("解析不了的流水不能参与匹配")
	}

	// 支付宝之后返回能解析的内容：按新内容替换，异常自动处理，订单照常到账。
	f.ingest(good)
	got := f.reload(o)
	if got.Status != store.OrderPaid || got.TxnID != tx.ID {
		t.Fatalf("能解析后应按新内容对账：%+v", got)
	}
	as := f.anomalies()
	if len(as) != 1 || as[0].Status != store.AnomalyResolved || as[0].ResolvedBy != "系统" {
		t.Fatalf("解析不了的异常应自动记为已处理：%+v", as)
	}
	if err := f.DB().NewSelect().Model(&tx).WherePK().Scan(f.Ctx); err != nil {
		t.Fatal(err)
	}
	if ledger.Invalid(&tx) || tx.Amount != good.Amount || tx.Fingerprint != ledger.Fingerprint(good) {
		t.Errorf("流水应换成新内容：%+v", tx)
	}

	// 已经入库的正常流水，再读到解析不了的版本时保留原记录、不记异常。
	f.ingest(bad)
	if n := len(f.anomalies()); n != 1 {
		t.Errorf("正常流水再读到坏内容时不该记异常，实际 %d 条", n)
	}
}

func TestLinkUnlinkResolve(t *testing.T) {
	f := newFixture(t)
	a := f.order("A", 1000)
	f.ingest(f.entry(1000, time.Now())) // 少付，记异常
	an := f.wantAnomaly(store.AnomalyUnderpaid, a.ID)

	if err := f.App.Ledger.Link(f.Ctx, an.TransactionID, a.ID, "admin", "客户说少付了一分，认了"); err != nil {
		t.Fatal(err)
	}
	got := f.reload(a)
	if got.Status != store.OrderPaid || got.PaidAmount != 1000 {
		t.Errorf("人工关联后应是已支付：%+v", got)
	}
	if as := f.anomalies(); as[0].Status != store.AnomalyResolved {
		t.Error("关联后异常应标为已处理")
	}
	if err := f.App.Ledger.Link(f.Ctx, an.TransactionID, a.ID, "admin", ""); err == nil {
		t.Error("已关联的流水不能再关联")
	}

	if err := f.App.Ledger.Unlink(f.Ctx, a.ID, "admin", "关联错了"); err != nil {
		t.Fatal(err)
	}
	got = f.reload(a)
	if got.Status != store.OrderDisputed || got.TxnID != 0 {
		t.Errorf("撤销后应是争议状态：%+v", got)
	}
	unl := f.wantAnomaly(store.AnomalyUnlinked, a.ID)
	var n store.Notification
	if err := f.DB().NewSelect().Model(&n).Where("order_id = ?", a.ID).Scan(f.Ctx); err != nil {
		t.Fatal(err)
	}
	if n.Status == store.NotifyPending {
		t.Error("撤销后应停止通知")
	}
	if order.ExternalStatus(got) != 0 {
		t.Error("争议订单对外应是未支付")
	}

	if err := f.App.Ledger.Resolve(f.Ctx, unl.ID, "admin", "已线下退款"); err != nil {
		t.Fatal(err)
	}
	if err := f.App.Ledger.Resolve(f.Ctx, unl.ID, "admin", "再来"); err == nil {
		t.Error("处理过的异常不能再处理")
	}
	var audits int64
	audits, _ = f.DB().NewSelect().Model((*store.AuditLog)(nil)).Count(f.Ctx)
	if audits < 3 {
		t.Errorf("人工操作应记审计，实际 %d 条", audits)
	}
}
