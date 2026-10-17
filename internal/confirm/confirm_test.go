package confirm_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/assistance"
	"tillbox/internal/channel"
	"tillbox/internal/channel/upstream"
	"tillbox/internal/confirm"
	"tillbox/internal/epay"
	"tillbox/internal/merchant"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// pair 是两套 Tillbox：down 用上游易支付渠道把订单转给 up 收款。
type pair struct {
	t    *testing.T
	down *apptest.Env
	up   *apptest.Env
	um   *apptest.Merchant // up 上的商户，down 的渠道用它对接
	dm   *apptest.Merchant // down 上的商户，用上游渠道
	ch   store.Channel
	a    *apptest.Admin
}

func setup(t *testing.T, version int) *pair {
	t.Helper()
	up, down := apptest.New(t), apptest.New(t)
	cfg := upstream.Input{APIURL: up.URL + "/", Version: version, Types: []string{"alipay"}}
	var um *apptest.Merchant
	if version == 1 {
		um = up.NewMerchant(nil)
		cfg.PID, cfg.Key = um.PID(), um.Key
	} else {
		um = up.NewRSAMerchant(true)
		priv, err := epay.MarshalPrivateKey(um.Priv)
		if err != nil {
			t.Fatal(err)
		}
		pf, err := up.App.Settings.Platform(up.Ctx)
		if err != nil {
			t.Fatal(err)
		}
		cfg.PID, cfg.PrivateKey, cfg.PlatformPublicKey = um.PID(), priv, pf.PublicKey
	}
	a := down.Login()
	res := a.Call("POST", "/web-api/admin/channels", map[string]any{"type": store.ChannelEpay, "name": "上游 A", "enabled": true, "config": cfg})
	if res.Status != http.StatusOK {
		t.Fatalf("建上游渠道：%d %s", res.Status, res.Body)
	}
	var out struct {
		Channel store.Channel `json:"channel"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatal(err)
	}
	dm := down.NewMerchant(&merchant.Input{Name: "下游商户", Enabled: true, ChannelID: out.Channel.ID})
	if dm.Channel != out.Channel.Key() {
		t.Fatalf("商户渠道 %s", dm.Channel)
	}
	return &pair{t: t, down: down, up: up, um: um, dm: dm, ch: out.Channel, a: a}
}

// order 在 down 下单，返回 down 的订单和 up 上对应的订单。
func (p *pair) order(out, money string) (*store.Order, *store.Order) {
	p.t.Helper()
	res := p.down.PostForm("/mapi.php", p.dm.SignMD5(map[string]string{
		"type": "alipay", "out_trade_no": out, "notify_url": p.down.Hook.URL + "/n", "name": "测试商品", "money": money, "clientip": "1.1.1.1",
	})).JSON(p.t)
	if fmt.Sprint(res["code"]) != "1" {
		p.t.Fatalf("下单：%v", res)
	}
	o := p.down.Order(fmt.Sprint(res["trade_no"]))
	uo, err := p.up.App.Orders.Find(p.up.Ctx, p.um.ID, "", o.TradeNo)
	if err != nil {
		p.t.Fatalf("上游没有对应订单：%v", err)
	}
	return o, uo
}

func (p *pair) payment(o *store.Order) *store.UpstreamPayment {
	p.t.Helper()
	up, err := p.down.App.Confirm.Payment(p.down.Ctx, o.ID)
	if err != nil || up == nil {
		p.t.Fatalf("没有预下单记录：%v", err)
	}
	return up
}

func txns(t *testing.T, e *apptest.Env, key string) []store.Transaction {
	t.Helper()
	var ts []store.Transaction
	if err := e.DB().NewSelect().Model(&ts).Where("channel = ?", key).Order("id ASC").Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	return ts
}

func anomalies(t *testing.T, e *apptest.Env, orderID int64) []store.Anomaly {
	t.Helper()
	var as []store.Anomaly
	if err := e.DB().NewSelect().Model(&as).Where("order_id = ?", orderID).Order("id ASC").Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	return as
}

// 回调确认：up 收到付款后通知 down，down 到账并通知自己的商户；重复回调幂等。
func TestNotifyConfirms(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("V%d", version), func(t *testing.T) {
			p := setup(t, version)
			o, uo := p.order("D1", "10.00")
			if o.PayAmount != 1000 || o.Amount != 1000 {
				t.Errorf("上游渠道按原价付款：%+v", o)
			}
			up := p.payment(o)
			if up.TradeNo != uo.TradeNo || up.PayType != "alipay" || up.InfoType != store.InfoQRCode || !strings.Contains(up.Info, "/cashier/"+uo.TradeNo) {
				t.Errorf("预下单记录：%+v", up)
			}
			// 同一订单号再下单：沿用原订单，不再预下单。
			again := p.down.PostForm("/mapi.php", p.dm.SignMD5(map[string]string{
				"type": "alipay", "out_trade_no": "D1", "notify_url": p.down.Hook.URL + "/n", "name": "测试商品", "money": "10.00", "clientip": "1.1.1.1",
			})).JSON(t)
			if fmt.Sprint(again["trade_no"]) != o.TradeNo {
				t.Errorf("重复下单应沿用原订单：%v", again)
			}

			p.up.Pay(uo.TradeNo)
			p.up.Deliver()
			got := p.down.Order(o.TradeNo)
			if got.Status != store.OrderPaid || got.PaidAmount != 1000 {
				t.Fatalf("回调后应到账：%+v", got)
			}
			ts := txns(t, p.down, p.ch.Key())
			if len(ts) != 1 || ts[0].ExternalID != uo.TradeNo || ts[0].OrderID != o.ID || ts[0].State != store.TxnMatched || ts[0].MerchantOrderNo != o.TradeNo {
				t.Fatalf("流水：%+v", ts)
			}
			if version == 2 && ts[0].AlipayOrderNo == "" {
				t.Error("V2 回调带支付宝交易号")
			}
			if strings.Contains(ts[0].Raw, "sign\"") {
				t.Error("流水原文不该带签名")
			}
			if as := anomalies(t, p.down, o.ID); len(as) != 0 {
				t.Errorf("按时付款不该有异常：%+v", as)
			}
			p.down.Deliver()
			if n := len(p.down.Hook.Got()); n != 1 {
				t.Fatalf("down 的商户应收到 1 次通知，实际 %d", n)
			}
			n := p.down.Hook.Got()[0].Params()
			if n["money"] != "10.00" || n["trade_no"] != o.TradeNo || n["type"] != "alipay" || !epay.MD5Verify(n, p.dm.Key) {
				t.Errorf("down 发给商户的通知：%v", n)
			}

			// up 手动重发通知：幂等，不重复入账。
			if _, err := p.up.DB().NewUpdate().Model((*store.Notification)(nil)).Set("status = ?", store.NotifyPending).Set("next_at = 0").Where("order_id = ?", uo.ID).Exec(p.up.Ctx); err != nil {
				t.Fatal(err)
			}
			p.up.Deliver()
			if ts := txns(t, p.down, p.ch.Key()); len(ts) != 1 {
				t.Errorf("重复回调不该再入账：%d 条流水", len(ts))
			}
			if st := p.down.App.Confirm.Status(p.ch.Key()); st.NotifyAt == 0 || st.NotifyErr != "" {
				t.Errorf("渠道状态：%+v", st)
			}
		})
	}
}

// 查单兜底：up 的通知没送到，down 定时查单确认到账。
func TestQueryConfirms(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("V%d", version), func(t *testing.T) {
			p := setup(t, version)
			o, uo := p.order("Q1", "5")
			if err := p.down.App.Confirm.RunOnce(p.down.Ctx); err != nil {
				t.Fatal(err)
			}
			up := p.payment(o)
			if up.CheckedAt == 0 || up.CheckError != "" {
				t.Errorf("未付款时查单：%+v", up)
			}
			if p.down.Order(o.TradeNo).Status != store.OrderPending {
				t.Fatal("没付款不该到账")
			}
			p.up.Pay(uo.TradeNo) // 付了款但不送通知
			// 刚查过，不到 15 秒不会再查；点“我已付款”后 3 秒就能再查。
			if err := p.down.App.Confirm.RunOnce(p.down.Ctx); err != nil {
				t.Fatal(err)
			}
			if p.down.Order(o.TradeNo).Status != store.OrderPending {
				t.Fatal("没到查单间隔不该查")
			}
			p.down.App.Confirm.Now = func() time.Time { return time.Now().Add(4 * time.Second) }
			res := p.down.PostForm("/web-api/cashier/"+o.TradeNo+"/paid", url.Values{})
			if res.Status != http.StatusOK {
				t.Fatalf("我已付款：%d", res.Status)
			}
			if err := p.down.App.Confirm.RunOnce(p.down.Ctx); err != nil {
				t.Fatal(err)
			}
			got := p.down.Order(o.TradeNo)
			if got.Status != store.OrderPaid || got.PaidAmount != 500 {
				t.Fatalf("查单后应到账：%+v", got)
			}
			ts := txns(t, p.down, p.ch.Key())
			if len(ts) != 1 || ts[0].ExternalID != uo.TradeNo || ts[0].OtherAccount == "" {
				t.Errorf("查单流水（带付款人）：%+v", ts)
			}
			// 之后 up 的通知到了：幂等。
			p.up.Deliver()
			if ts := txns(t, p.down, p.ch.Key()); len(ts) != 1 {
				t.Errorf("查单后再回调不该重复入账：%d", len(ts))
			}
		})
	}
}

// 过期后才确认：照样到账，另记一条待处理的迟付异常提醒核对商户入账。
func TestLateConfirmation(t *testing.T) {
	p := setup(t, 1)
	o, uo := p.order("L1", "3.00")
	past := store.Ms(time.Now().Add(-time.Minute))
	if _, err := p.down.DB().NewUpdate().Model((*store.Order)(nil)).Set("expires_at = ?", past).Where("id = ?", o.ID).Exec(p.down.Ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.down.App.Orders.ExpireDue(p.down.Ctx); err != nil {
		t.Fatal(err)
	}
	p.up.Pay(uo.TradeNo)
	p.up.Deliver()
	got := p.down.Order(o.TradeNo)
	if got.Status != store.OrderPaid {
		t.Fatalf("过期后确认也要到账：%+v", got)
	}
	as := anomalies(t, p.down, o.ID)
	if len(as) != 1 || as[0].Kind != store.AnomalyLate || as[0].Status != store.AnomalyOpen || !strings.Contains(as[0].Detail, "商户后台核对") {
		t.Fatalf("应记一条待处理的迟付异常：%+v", as)
	}
	if ts := txns(t, p.down, p.ch.Key()); ts[0].State != store.TxnMatched {
		t.Errorf("流水已关联订单：%+v", ts[0])
	}
}

// 金额不符、关闭后付款、重复付款都只记异常，不到账。
func TestConfirmAnomalies(t *testing.T) {
	p := setup(t, 1)
	l := p.down.App.Ledger
	ctx := p.down.Ctx

	o, _ := p.order("A1", "8.00")
	ok, err := l.Confirm(ctx, o.ID, channel.Paid{TradeNo: "U-1", OutTradeNo: o.TradeNo, Type: "alipay", Amount: 700})
	if err != nil || ok {
		t.Fatalf("少付不该到账：%v %v", ok, err)
	}
	if as := anomalies(t, p.down, o.ID); len(as) != 1 || as[0].Kind != store.AnomalyUnderpaid {
		t.Errorf("少付异常：%+v", as)
	}
	if p.down.Order(o.TradeNo).Status != store.OrderPending {
		t.Error("少付后订单仍待支付")
	}
	// 同一个上游单号再来一次：幂等，不再记异常。
	if ok, err := l.Confirm(ctx, o.ID, channel.Paid{TradeNo: "U-1", OutTradeNo: o.TradeNo, Amount: 700}); err != nil || ok {
		t.Fatal(ok, err)
	}
	if as := anomalies(t, p.down, o.ID); len(as) != 1 {
		t.Errorf("重复确认不该再记异常：%+v", as)
	}
	// 正确金额到账，再来一个不同的上游单号：重复付款。
	if ok, err := l.Confirm(ctx, o.ID, channel.Paid{TradeNo: "U-2", OutTradeNo: o.TradeNo, Amount: 800}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := l.Confirm(ctx, o.ID, channel.Paid{TradeNo: "U-3", OutTradeNo: o.TradeNo, Amount: 800}); err != nil || ok {
		t.Fatal(ok, err)
	}
	if as := anomalies(t, p.down, o.ID); len(as) != 2 || as[1].Kind != store.AnomalyDuplicate {
		t.Errorf("重复付款异常：%+v", as)
	}

	c, _ := p.order("A2", "2.00")
	if _, err := p.down.App.Orders.Close(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := l.Confirm(ctx, c.ID, channel.Paid{TradeNo: "U-4", OutTradeNo: c.TradeNo, Amount: 200}); err != nil || ok {
		t.Fatal(ok, err)
	}
	if as := anomalies(t, p.down, c.ID); len(as) != 1 || as[0].Kind != store.AnomalyLate || !strings.Contains(as[0].Detail, "关闭") {
		t.Errorf("关闭后付款：%+v", as)
	}
	if p.down.Order(c.TradeNo).Status != store.OrderClosed {
		t.Error("关闭的订单不自动到账")
	}
}

// 不合格的回调回 fail，不到账。
func TestBadNotify(t *testing.T) {
	p := setup(t, 1)
	o, uo := p.order("B1", "1.00")
	path := fmt.Sprintf("/web-api/channels/%d/notify?", p.ch.ID)
	sign := func(params epay.Params) string {
		params["sign"] = epay.MD5Sign(params, p.um.Key)
		params["sign_type"] = "MD5"
		v := url.Values{}
		for k, s := range params {
			v.Set(k, s)
		}
		return v.Encode()
	}
	base := func() epay.Params {
		return epay.Params{"pid": p.um.PID(), "trade_no": uo.TradeNo, "out_trade_no": o.TradeNo, "type": "alipay", "name": "测试商品", "money": "1", "trade_status": "TRADE_SUCCESS"}
	}
	for _, c := range []struct {
		name  string
		query string
		code  int
	}{
		{"签名错", strings.Replace(sign(base()), "sign=", "sign=0", 1), http.StatusBadRequest},
		{"商户号不符", func() string { b := base(); b["pid"] = "999"; return sign(b) }(), http.StatusBadRequest},
		{"订单不存在", func() string { b := base(); b["out_trade_no"] = "20991231000000000000"; return sign(b) }(), http.StatusBadRequest},
		{"不是成功状态", func() string { b := base(); b["trade_status"] = "WAIT"; return sign(b) }(), http.StatusBadRequest},
	} {
		res := p.down.Get(path + c.query)
		if res.Status != c.code || string(res.Body) != "fail" {
			t.Errorf("%s：%d %s", c.name, res.Status, res.Body)
		}
	}
	if res := p.down.Get(fmt.Sprintf("/web-api/channels/%d/notify?", p.down.Demo.Row().ID) + sign(base())); res.Status != http.StatusBadRequest {
		t.Errorf("演示渠道没有回调：%d", res.Status)
	}
	if p.down.Order(o.TradeNo).Status != store.OrderPending {
		t.Fatal("不合格的回调不该到账")
	}
	if st := p.down.App.Confirm.Status(p.ch.Key()); st.NotifyErr == "" {
		t.Errorf("渠道状态应记下回调错误：%+v", st)
	}
	// 合格的回调（金额按官方去掉末尾的 0）。
	res := p.down.Get(path + sign(base()))
	if res.Status != http.StatusOK || string(res.Body) != "success" {
		t.Fatalf("合格回调：%d %s", res.Status, res.Body)
	}
	if p.down.Order(o.TradeNo).Status != store.OrderPaid {
		t.Fatal("合格回调应到账")
	}
}

// 预下单失败：向调用方报通道不可用，订单保持待支付；上游恢复后用同一订单号重试会再预下单。
func TestPrepayFailure(t *testing.T) {
	p := setup(t, 1)
	// 把 up 上的商户停用，上游会拒绝下单。
	if _, err := p.up.DB().NewUpdate().Model((*store.Merchant)(nil)).Set("enabled = ?", false).Where("id = ?", p.um.ID).Exec(p.up.Ctx); err != nil {
		t.Fatal(err)
	}
	form := p.dm.SignMD5(map[string]string{"type": "alipay", "out_trade_no": "F1", "notify_url": p.down.Hook.URL + "/n", "name": "测试商品", "money": "1", "clientip": "1.1.1.1"})
	res := p.down.PostForm("/mapi.php", form).JSON(t)
	if fmt.Sprint(res["code"]) != "-2" || !strings.Contains(fmt.Sprint(res["msg"]), "暂时不可用") {
		t.Fatalf("预下单失败：%v", res)
	}
	if st := p.down.App.Confirm.Status(p.ch.Key()); st.PrepayErr == "" || st.Failures != 1 {
		t.Errorf("渠道状态应记下预下单错误：%+v", st)
	}
	if _, err := p.up.DB().NewUpdate().Model((*store.Merchant)(nil)).Set("enabled = ?", true).Where("id = ?", p.um.ID).Exec(p.up.Ctx); err != nil {
		t.Fatal(err)
	}
	res = p.down.PostForm("/mapi.php", form).JSON(t)
	if fmt.Sprint(res["code"]) != "1" {
		t.Fatalf("重试下单：%v", res)
	}
	o := p.down.Order(fmt.Sprint(res["trade_no"]))
	if up := p.payment(o); up.TradeNo == "" {
		t.Errorf("重试后应有预下单记录：%+v", up)
	}
}

func postJSON(t *testing.T, e *apptest.Env, path string, body any) (*apptest.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, e.URL+path, strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res := e.Do(req)
	var m map[string]any
	_ = json.Unmarshal(res.Body, &m)
	return res, m
}

// 渠道开放多种方式、调用方没传 type：付款人在收银台选定后才预下单。
func TestCashierChoosesType(t *testing.T) {
	p := setup(t, 1)
	cfg := upstream.Input{APIURL: p.up.URL, Version: 1, PID: p.um.PID(), Types: []string{"alipay", "wxpay"}}
	res := p.a.Call("PUT", fmt.Sprintf("/web-api/admin/channels/%d", p.ch.ID), map[string]any{"name": "上游 A", "enabled": true, "config": cfg})
	if res.Status != http.StatusOK {
		t.Fatalf("改渠道：%d %s", res.Status, res.Body)
	}
	form := p.dm.SignMD5(map[string]string{"out_trade_no": "S1", "notify_url": p.down.Hook.URL + "/n", "return_url": p.down.Hook.URL + "/r", "name": "测试商品", "money": "2.00"})
	if res := p.down.PostForm("/submit.php", form); res.Status != http.StatusSeeOther {
		t.Fatalf("跳转下单：%d %s", res.Status, res.Body)
	}
	o, err := p.down.App.Orders.Find(p.down.Ctx, p.dm.ID, "", "S1")
	if err != nil {
		t.Fatal(err)
	}
	if o.PayType != "" {
		t.Errorf("没传 type 时留给付款人选：%q", o.PayType)
	}
	if up, _ := p.down.App.Confirm.Payment(p.down.Ctx, o.ID); up != nil {
		t.Fatal("选支付方式之前不该预下单")
	}
	view := p.down.Get("/web-api/cashier/" + o.TradeNo).JSON(t)
	if types, _ := view["pay_types"].([]any); len(types) != 2 || view["guide"] != nil || view["pay_amount"] != "2.00" {
		t.Fatalf("收银台：%v", view)
	}

	base := "/web-api/cashier/" + o.TradeNo
	if r, m := postJSON(t, p.down, base+"/pay", map[string]string{"type": "qqpay"}); r.Status != http.StatusBadRequest {
		t.Errorf("没开放的方式：%d %v", r.Status, m)
	}
	// up 只收支付宝：选微信时上游拒绝。
	if r, m := postJSON(t, p.down, base+"/pay", map[string]string{"type": "wxpay"}); r.Status != http.StatusBadGateway || !strings.Contains(fmt.Sprint(m["error"]), "换一种支付方式") {
		t.Errorf("上游拒绝：%d %v", r.Status, m)
	}
	r, m := postJSON(t, p.down, base+"/pay", map[string]string{"type": "alipay"})
	g, _ := m["guide"].(map[string]any)
	if r.Status != http.StatusOK || g == nil || !strings.Contains(fmt.Sprint(g["qrcode"]), "/cashier/") || g["app"] != "支付宝" {
		t.Fatalf("选支付宝：%d %v", r.Status, m)
	}
	view = p.down.Get(base).JSON(t)
	if view["pay_type"] != "alipay" || view["guide"] == nil {
		t.Errorf("选定后收银台：%v", view)
	}
	if r, m := postJSON(t, p.down, base+"/go", nil); r.Status != http.StatusOK || m["url"] != g["qrcode"] {
		t.Errorf("去付款：%d %v", r.Status, m)
	}
	// 再换微信被上游拒绝：订单还是支付宝，原来的付款信息照样能用。
	if r, m := postJSON(t, p.down, base+"/pay", map[string]string{"type": "wxpay"}); r.Status != http.StatusBadGateway {
		t.Errorf("换微信：%d %v", r.Status, m)
	}
	if view := p.down.Get(base).JSON(t); view["pay_type"] != "alipay" || view["guide"] == nil {
		t.Errorf("换微信失败后收银台：%v", view)
	}
	if r, m := postJSON(t, p.down, base+"/go", nil); r.Status != http.StatusOK || m["url"] != g["qrcode"] {
		t.Errorf("换微信失败后去付款：%d %v", r.Status, m)
	}
	// 旧版本留下的订单：支付方式被失败的预下单改掉了，选回原来的方式时改回来。
	if _, err := p.down.DB().NewUpdate().Model((*store.Order)(nil)).Set("pay_type = ?", "wxpay").Where("id = ?", o.ID).Exec(p.down.Ctx); err != nil {
		t.Fatal(err)
	}
	if r, m := postJSON(t, p.down, base+"/pay", map[string]string{"type": "alipay"}); r.Status != http.StatusOK || m["pay_type"] != "alipay" {
		t.Errorf("选回支付宝：%d %v", r.Status, m)
	}
	if view := p.down.Get(base).JSON(t); view["pay_type"] != "alipay" || view["guide"] == nil {
		t.Errorf("选回支付宝后收银台：%v", view)
	}
	// 付款后通知里带上付款人选的方式。
	uo, err := p.up.App.Orders.Find(p.up.Ctx, p.um.ID, "", o.TradeNo)
	if err != nil {
		t.Fatal(err)
	}
	p.up.Pay(uo.TradeNo)
	p.up.Deliver()
	p.down.Deliver()
	if got := p.down.Hook.Got(); len(got) != 1 || got[0].Params()["type"] != "alipay" {
		t.Errorf("通知：%v", got)
	}
	if r, _ := postJSON(t, p.down, base+"/pay", map[string]string{"type": "alipay"}); r.Status != http.StatusConflict {
		t.Errorf("已支付后再选方式：%d", r.Status)
	}
}

// 上游的单笔限额：被拒时自动识别，之后下单和收银台提前拦下并说明原因；过期后交给上游，下单成功就放宽。
func TestUpstreamLimits(t *testing.T) {
	p := setup(t, 1)
	// 在 up 前面挡一层：低于最低金额的预下单按彩虹易支付的原话拒绝，其余转给 up。
	target, err := url.Parse(p.up.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	var mu sync.Mutex
	floor, calls := int64(30), 0
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mapi.php" {
			_ = r.ParseForm()
			cents, _ := epay.ParseMoney(r.PostForm.Get("money"))
			mu.Lock()
			calls++
			lo := floor
			mu.Unlock()
			if cents < lo {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": -2, "msg": "最小支付金额是0.3元"})
				return
			}
			body := r.PostForm.Encode()
			r.Body, r.ContentLength = io.NopCloser(strings.NewReader(body)), int64(len(body))
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	cfg := upstream.Input{APIURL: front.URL, Version: 1, PID: p.um.PID(), Key: p.um.Key, Types: []string{"alipay", "wxpay"}}
	if res := p.a.Call("PUT", fmt.Sprintf("/web-api/admin/channels/%d", p.ch.ID), map[string]any{"name": "上游 A", "enabled": true, "config": cfg}); res.Status != http.StatusOK {
		t.Fatalf("改渠道：%d %s", res.Status, res.Body)
	}
	mapi := func(out, typ, money string) map[string]any {
		t.Helper()
		params := map[string]string{"out_trade_no": out, "notify_url": p.down.Hook.URL + "/n", "name": "测试商品", "money": money, "clientip": "1.1.1.1"}
		if typ != "" {
			params["type"] = typ
		}
		return p.down.PostForm("/mapi.php", p.dm.SignMD5(params)).JSON(t)
	}
	limits := func() map[string]upstream.LimitView {
		t.Helper()
		var list struct {
			Items []struct {
				ID     int64 `json:"id"`
				Config struct {
					Limits map[string]upstream.LimitView `json:"limits"`
				} `json:"config"`
			} `json:"items"`
		}
		res := p.a.Call("GET", "/web-api/admin/channels", nil)
		if err := json.Unmarshal(res.Body, &list); err != nil {
			t.Fatal(err)
		}
		for _, c := range list.Items {
			if c.ID == p.ch.ID {
				return c.Config.Limits
			}
		}
		t.Fatal("渠道列表里没有上游渠道")
		return nil
	}

	// 第一次：上游拒绝，原因直接告诉调用方；记下限额，不算渠道失败。
	if res := mapi("L1", "alipay", "0.01"); fmt.Sprint(res["code"]) != "-1" || res["msg"] != "支付宝单笔最低 0.30 元" || count() != 1 {
		t.Fatalf("低于上游最低金额：%v，上游收到 %d 次", res, count())
	}
	if st := p.down.App.Confirm.Status(p.ch.Key()); st.Failures != 0 || st.PrepayErr != "" {
		t.Errorf("金额被拒不算渠道失败：%+v", st)
	}
	if l := limits()["alipay"]; l.Min != "0.30" || l.MinAt == 0 {
		t.Fatalf("后台看到的限额：%+v", limits())
	}
	// 再下同样金额的单：不问上游，也不建单；够最低金额的照常。
	if res := mapi("L2", "alipay", "0.01"); fmt.Sprint(res["code"]) != "-1" || res["msg"] != "支付宝单笔最低 0.30 元" || count() != 1 {
		t.Fatalf("已知限额：%v，上游收到 %d 次", res, count())
	}
	if _, err := p.down.App.Orders.Find(p.down.Ctx, p.dm.ID, "", "L2"); err == nil {
		t.Error("已知超出限额时不该建单")
	}
	if res := mapi("L3", "alipay", "0.30"); fmt.Sprint(res["code"]) != "1" || count() != 2 {
		t.Fatalf("够最低金额：%v", res)
	}

	// 收银台：没指定方式的订单，支付宝标成不可用，微信照常可选。
	form := p.dm.SignMD5(map[string]string{"out_trade_no": "L4", "notify_url": p.down.Hook.URL + "/n", "return_url": p.down.Hook.URL + "/r", "name": "测试商品", "money": "0.01"})
	if res := p.down.PostForm("/submit.php", form); res.Status != http.StatusSeeOther {
		t.Fatalf("跳转下单：%d %s", res.Status, res.Body)
	}
	o, err := p.down.App.Orders.Find(p.down.Ctx, p.dm.ID, "", "L4")
	if err != nil {
		t.Fatal(err)
	}
	base := "/web-api/cashier/" + o.TradeNo
	unavailable := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		types, _ := p.down.Get(base).JSON(t)["pay_types"].([]any)
		for _, x := range types {
			m, _ := x.(map[string]any)
			out[fmt.Sprint(m["type"])], _ = m["unavailable"].(string)
		}
		return out
	}
	if u := unavailable(); len(u) != 2 || u["alipay"] != "单笔最低 0.30 元" || u["wxpay"] != "" {
		t.Fatalf("收银台的支付方式：%v", u)
	}
	if r, m := postJSON(t, p.down, base+"/pay", map[string]string{"type": "alipay"}); r.Status != http.StatusUnprocessableEntity || m["error"] != "支付宝单笔最低 0.30 元，请换一种支付方式" || count() != 2 {
		t.Errorf("选支付宝：%d %v，上游收到 %d 次", r.Status, m, count())
	}
	if r, m := postJSON(t, p.down, base+"/pay", map[string]string{"type": "wxpay"}); r.Status != http.StatusUnprocessableEntity || m["error"] != "微信支付单笔最低 0.30 元，请换一种支付方式" || count() != 3 {
		t.Errorf("选微信：%d %v，上游收到 %d 次", r.Status, m, count())
	}
	// 每种方式都低于最低金额：不建单，说明各自的限额。
	form = p.dm.SignMD5(map[string]string{"out_trade_no": "L5", "notify_url": p.down.Hook.URL + "/n", "return_url": p.down.Hook.URL + "/r", "name": "测试商品", "money": "0.01"})
	if res := p.down.PostForm("/submit.php", form); !strings.Contains(string(res.Body), "支付宝单笔最低 0.30 元，微信支付单笔最低 0.30 元") {
		t.Errorf("每种方式都超出限额：%d %s", res.Status, res.Body)
	}

	// 过了 24 小时：不再提前拦下；上游已经放宽，下单成功后清掉支付宝的限额。
	var row store.Channel
	if err := p.down.DB().NewSelect().Model(&row).Where("id = ?", p.ch.ID).Scan(p.down.Ctx); err != nil {
		t.Fatal(err)
	}
	c, err := upstream.ParseConfig(row.Config)
	if err != nil {
		t.Fatal(err)
	}
	for typ, l := range c.Limits {
		l.MinAt -= (upstream.LimitTrust + time.Hour).Milliseconds()
		c.Limits[typ] = l
	}
	b, _ := json.Marshal(c)
	if _, err := p.down.DB().NewUpdate().Model((*store.Channel)(nil)).Set("config = ?", string(b)).Where("id = ?", p.ch.ID).Exec(p.down.Ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.down.App.Channels.Load(p.down.Ctx); err != nil {
		t.Fatal(err)
	}
	if u := unavailable(); u["alipay"] != "" || u["wxpay"] != "" {
		t.Errorf("限额过期后：%v", u)
	}
	mu.Lock()
	floor = 0
	mu.Unlock()
	if r, m := postJSON(t, p.down, base+"/pay", map[string]string{"type": "alipay"}); r.Status != http.StatusOK || count() != 4 {
		t.Fatalf("上游放宽后选支付宝：%d %v", r.Status, m)
	}
	if l := limits(); len(l) != 1 || l["wxpay"].Min != "0.30" {
		t.Errorf("下单成功后放宽支付宝，微信不变：%+v", l)
	}
}

// 付款人申诉上游订单“未到账”：AI 只能请求查单，不能用流水补认；查单确认后自动到账。
func TestAssistanceRechecksUpstream(t *testing.T) {
	p := setup(t, 1)
	o, uo := p.order("AI1", "6.00")
	var allowed []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) != 2 {
			t.Errorf("模型请求：%v", err)
			return
		}
		var facts struct {
			Allowed []string `json:"allowed_actions"`
		}
		_ = json.Unmarshal([]byte(req.Messages[1].Content), &facts)
		allowed = facts.Allowed
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"action":"recheck","reason":"请上游查单"}`}}}})
	}))
	t.Cleanup(model.Close)
	cfg := settings.Automation{Enabled: true, BaseURL: model.URL + "/v1", Model: "m", APIKey: "k", AutoLink: true, AutoNotify: true, AutoRecheck: true, IntervalSeconds: 30, MaxAttempts: 2}
	if err := p.down.App.Settings.SaveAutomation(p.down.Ctx, cfg); err != nil {
		t.Fatal(err)
	}
	// 先查过一次单（还没付款），之后不到 15 秒不会自己再查。
	if err := p.down.App.Confirm.RunOnce(p.down.Ctx); err != nil {
		t.Fatal(err)
	}
	p.up.Pay(uo.TradeNo) // 上游已收款，但回调没送到
	_, c, err := p.down.App.Assistance.Submit(p.down.Ctx, o.ID, assistance.ClaimInput{
		Kind: "not_received", PaymentRef: "2026101700000000000009", Amount: o.PayAmount, PaidAt: store.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.down.App.Assistance.RunOnce(p.down.Ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(allowed, []string{"manual", "recheck"}) {
		t.Errorf("上游订单可选动作：%v", allowed)
	}
	var got store.AssistanceCase
	if err := p.down.DB().NewSelect().Model(&got).Where("id = ?", c.ID).Scan(p.down.Ctx); err != nil {
		t.Fatal(err)
	}
	if got.Status != "waiting" {
		t.Fatalf("申诉应等待查单结果：%+v", got)
	}
	p.down.App.Confirm.Now = func() time.Time { return time.Now().Add(4 * time.Second) }
	if err := p.down.App.Confirm.RunOnce(p.down.Ctx); err != nil {
		t.Fatal(err)
	}
	if st := p.down.Order(o.TradeNo).Status; st != store.OrderPaid {
		t.Fatalf("AI 触发查单后应到账：%s", st)
	}
}

// 后台：订单详情带上游预下单记录和渠道名；订单和流水能按渠道筛选；渠道列表带上游状态。
func TestAdminViews(t *testing.T) {
	p := setup(t, 1)
	o, uo := p.order("V1", "2.00")
	p.up.Pay(uo.TradeNo)
	p.up.Deliver()
	get := func(path string, out any) {
		t.Helper()
		res := p.a.Call("GET", path, nil)
		if res.Status != http.StatusOK {
			t.Fatalf("%s：%d %s", path, res.Status, res.Body)
		}
		if err := json.Unmarshal(res.Body, out); err != nil {
			t.Fatal(err)
		}
	}
	var detail struct {
		Order struct {
			ChannelName string `json:"channel_name"`
		} `json:"order"`
		Upstream *store.UpstreamPayment `json:"upstream"`
	}
	get(fmt.Sprintf("/web-api/admin/orders/%d", o.ID), &detail)
	if detail.Order.ChannelName != "上游 A" || detail.Upstream == nil || detail.Upstream.TradeNo != uo.TradeNo {
		t.Errorf("订单详情：%+v %+v", detail.Order, detail.Upstream)
	}
	var page struct {
		Total int `json:"total"`
	}
	for id, want := range map[int64]int{p.ch.ID: 1, p.ch.ID + 100: 0} {
		get(fmt.Sprintf("/web-api/admin/orders?channel_id=%d", id), &page)
		if page.Total != want {
			t.Errorf("按渠道 %d 筛选订单：%d", id, page.Total)
		}
		get(fmt.Sprintf("/web-api/admin/transactions?channel_id=%d", id), &page)
		if page.Total != want {
			t.Errorf("按渠道 %d 筛选流水：%d", id, page.Total)
		}
	}
	var list struct {
		Items []struct {
			ID       int64           `json:"id"`
			PayTypes []string        `json:"pay_types"`
			Upstream *confirm.Status `json:"upstream"`
			Orders   int64           `json:"orders"`
		} `json:"items"`
	}
	get("/web-api/admin/channels", &list)
	found := false
	for _, c := range list.Items {
		if c.ID == p.ch.ID {
			found = true
			if c.Upstream == nil || c.Upstream.NotifyAt == 0 || c.Orders != 1 || !slices.Equal(c.PayTypes, []string{"alipay"}) {
				t.Errorf("渠道列表：%+v", c)
			}
		}
	}
	if !found {
		t.Error("渠道列表里没有上游渠道")
	}
}
