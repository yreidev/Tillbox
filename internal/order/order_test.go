package order_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/channel/registry"
	"tillbox/internal/merchant"
	"tillbox/internal/order"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

func input(m *apptest.Merchant, no string, cents int64) order.CreateInput {
	return order.CreateInput{
		MerchantID: m.ID, OutTradeNo: no, Version: order.V1, Channel: m.Channel, PayType: "alipay",
		Name: "x", Money: fmt.Sprintf("%d.%02d", cents/100, cents%100), Amount: cents,
		NotifyURL: "https://example.com/n", ClientIP: "127.0.0.1", Device: "pc",
	}
}

// 并发下单时应付金额不能重复。
func TestConcurrentAllocation(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	const n = 60
	var wg sync.WaitGroup
	results := make([]*store.Order, n)
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			results[i], _, errs[i] = e.App.Orders.Create(e.Ctx, input(m, fmt.Sprintf("C%03d", i), 1000))
		})
	}
	wg.Wait()
	seen := map[int64]bool{}
	tradeNos := map[string]bool{}
	for i, o := range results {
		if errs[i] != nil {
			t.Fatalf("第 %d 单失败：%v", i, errs[i])
		}
		if o.PayAmount <= 1000 || o.PayAmount > 1099 {
			t.Errorf("应付金额超出范围：%d", o.PayAmount)
		}
		if seen[o.PayAmount] {
			t.Errorf("应付金额重复：%d", o.PayAmount)
		}
		seen[o.PayAmount] = true
		if tradeNos[o.TradeNo] {
			t.Errorf("平台订单号重复：%s", o.TradeNo)
		}
		tradeNos[o.TradeNo] = true
	}
	// 分配是从小到大连续的。
	for k := int64(1001); k <= 1000+n; k++ {
		if !seen[k] {
			t.Errorf("没有分到 %d", k)
		}
	}
}

func TestPoolFullAndRelease(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	pay := settings.DefaultPayment()
	pay.MaxTailCents = 3
	if err := e.App.Settings.SavePayment(e.Ctx, pay); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.App.Orders.Now = func() time.Time { return now }
	var first *store.Order
	for i := range 3 {
		o, _, err := e.App.Orders.Create(e.Ctx, input(m, fmt.Sprintf("P%d", i), 500))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = o
		}
	}
	_, _, err := e.App.Orders.Create(e.Ctx, input(m, "P9", 500))
	var full *order.PoolFullError
	if !errors.As(err, &full) {
		t.Fatalf("应报金额池满：%v", err)
	}
	wantRetry := now.Add(pay.OrderTTL() + pay.Cooldown())
	if d := full.RetryAt.Sub(wantRetry); d > time.Second || d < -time.Second {
		t.Errorf("重试时间 %v，应为 %v", full.RetryAt, wantRetry)
	}
	// 别的金额不受影响。
	if _, _, err := e.App.Orders.Create(e.Ctx, input(m, "P10", 600)); err != nil {
		t.Errorf("别的金额应能下单：%v", err)
	}

	// 关掉一单：冷却期内仍占着，冷却过后释放。
	if _, err := e.App.Orders.Close(e.Ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.App.Orders.Create(e.Ctx, input(m, "P11", 500)); !errors.As(err, &full) {
		t.Errorf("冷却期内不该释放：%v", err)
	}
	e.App.Orders.Now = func() time.Time { return now.Add(pay.Cooldown() + time.Second) }
	o, _, err := e.App.Orders.Create(e.Ctx, input(m, "P12", 500))
	if err != nil {
		t.Fatalf("冷却过后应能下单：%v", err)
	}
	if o.PayAmount != first.PayAmount {
		t.Errorf("应复用释放的 %d，实际 %d", first.PayAmount, o.PayAmount)
	}
}

func TestExpireAndClose(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	o, _, err := e.App.Orders.Create(e.Ctx, input(m, "X1", 100))
	if err != nil {
		t.Fatal(err)
	}
	if order.Effective(o, time.Now()) != store.OrderPending {
		t.Error("新订单应是待支付")
	}
	later := time.Now().Add(10 * time.Minute)
	if order.Effective(o, later) != store.OrderExpired {
		t.Error("到期后应视为已过期（即使还没跑 ExpireDue）")
	}
	e.App.Orders.Now = func() time.Time { return later }
	n, err := e.App.Orders.ExpireDue(e.Ctx)
	if err != nil || n != 1 {
		t.Fatalf("ExpireDue：%d %v", n, err)
	}
	got, _ := e.App.Orders.ByID(e.Ctx, o.ID)
	if got.Status != store.OrderExpired || got.EndedAt != got.ExpiresAt {
		t.Errorf("过期后的订单：%+v", got)
	}
	// 过期的订单再关也算成功，状态不变。
	if c, err := e.App.Orders.Close(e.Ctx, o.ID); err != nil || c.Status != store.OrderExpired {
		t.Errorf("关闭已过期订单：%v %v", c, err)
	}
	if order.ExternalStatus(got) != 0 {
		t.Error("过期订单对外状态应是 0")
	}
}

// 同一商户订单号换了接口版本不能复用原订单：两个版本的通知签名方式不同。
func TestReuseRequiresSameVersion(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	first, _, err := e.App.Orders.Create(e.Ctx, input(m, "V001", 1000))
	if err != nil {
		t.Fatal(err)
	}
	again, reused, err := e.App.Orders.Create(e.Ctx, input(m, "V001", 1000))
	if err != nil || !reused || again.TradeNo != first.TradeNo {
		t.Fatalf("同样参数应复用原订单：%v %v", reused, err)
	}
	v2 := input(m, "V001", 1000)
	v2.Version = order.V2
	if _, _, err := e.App.Orders.Create(e.Ctx, v2); err == nil {
		t.Fatal("换了接口版本仍复用了原订单")
	}
}

// 金额池按渠道分配：不同渠道的同额订单可以同时用相同的应付金额。
func TestAllocationPerChannel(t *testing.T) {
	e := apptest.New(t)
	a := e.NewMerchant(nil)
	second, err := e.App.Channels.Create(e.Ctx, registry.Input{Type: store.ChannelDemo, Name: "演示 2", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	b := e.NewMerchant(&merchant.Input{Name: "B", Enabled: true, ChannelID: second.ID})
	if a.Channel == b.Channel || b.Channel != second.Key() {
		t.Fatalf("两个商户应在不同渠道：%s %s", a.Channel, b.Channel)
	}
	oa, _, err := e.App.Orders.Create(e.Ctx, input(a, "A1", 1000))
	if err != nil {
		t.Fatal(err)
	}
	ob, _, err := e.App.Orders.Create(e.Ctx, input(b, "B1", 1000))
	if err != nil {
		t.Fatal(err)
	}
	oa2, _, err := e.App.Orders.Create(e.Ctx, input(a, "A2", 1000))
	if err != nil {
		t.Fatal(err)
	}
	if oa.PayAmount != 1001 || ob.PayAmount != 1001 || oa2.PayAmount != 1002 {
		t.Errorf("应付金额：%d %d %d", oa.PayAmount, ob.PayAmount, oa2.PayAmount)
	}
}
