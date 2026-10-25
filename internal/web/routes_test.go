package web_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"tillbox/internal/apptest"
	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/merchant"
	"tillbox/internal/store"
)

func TestTypeOrderCoversNames(t *testing.T) {
	if got, want := slices.Sorted(slices.Values(channel.TypeOrder)), slices.Sorted(maps.Keys(channel.TypeNames)); !slices.Equal(got, want) {
		t.Errorf("TypeOrder %v 和 TypeNames %v 不一致", got, want)
	}
}

// routed 是一套按支付方式分渠道的环境：支付宝走演示渠道（按金额推断到账），微信走第三方易支付（假的第三方平台，预下单总是成功）。
type routed struct {
	e     *apptest.Env
	a     *apptest.Admin
	m     *apptest.Merchant
	epay  store.Channel
	upURL string // 假的第三方平台的接口地址
	mu    sync.Mutex
	calls []string // 第三方平台收到的预下单的支付方式
}

func newRouted(t *testing.T) *routed {
	t.Helper()
	r := &routed{e: apptest.New(t)}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		r.mu.Lock()
		r.calls = append(r.calls, req.PostForm.Get("type"))
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "trade_no": "UP" + req.PostForm.Get("out_trade_no"), "payurl": "https://up.example.com/pay"})
	}))
	t.Cleanup(up.Close)
	r.upURL = up.URL + "/"
	r.a = r.e.Login()
	res := r.a.Call("POST", "/web-api/admin/channels", map[string]any{"type": store.ChannelEpay, "name": "第三方", "enabled": true,
		"config": map[string]any{"api_url": r.upURL, "version": 1, "pid": "1000", "key": "k", "types": []string{"alipay", "wxpay"}}})
	if res.Status != http.StatusOK {
		t.Fatalf("建第三方易支付渠道：%d %s", res.Status, res.Body)
	}
	var out struct {
		Channel store.Channel `json:"channel"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatal(err)
	}
	r.epay = out.Channel
	r.m = r.e.NewMerchant(&merchant.Input{Name: "分渠道商户", Enabled: true, Routes: map[string]int64{"alipay": r.e.Demo.Row().ID, "wxpay": r.epay.ID}})
	return r
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

func (r *routed) prepays() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func (r *routed) params(out, typ string) map[string]string {
	p := map[string]string{"out_trade_no": out, "notify_url": r.e.Hook.URL + "/n", "return_url": r.e.Hook.URL + "/r", "name": "测试商品", "money": "5.00", "clientip": "1.1.1.1"}
	if typ != "" {
		p["type"] = typ
	}
	return p
}

// submit 跳转下单，返回平台订单号；没跳到收银台时返回空串和页面内容。
func (r *routed) submit(t *testing.T, out, typ string) (string, string) {
	t.Helper()
	res := r.e.PostForm("/submit.php", r.m.SignMD5(r.params(out, typ)))
	if res.Status != http.StatusSeeOther {
		return "", string(res.Body)
	}
	loc := res.Header.Get("Location")
	return loc[strings.LastIndex(loc, "/")+1:], ""
}

func TestOrderRoutesByType(t *testing.T) {
	r := newRouted(t)
	e := r.e

	// 带了支付方式：按商户为这种方式选的渠道建单。
	alipay := e.Order(create(t, e, r.m, r.params("R1", "alipay")))
	if alipay.Channel != e.ChannelKey() || alipay.PayType != "alipay" || alipay.PayAmount == 0 {
		t.Errorf("支付宝订单：%+v", alipay)
	}
	wx := e.Order(create(t, e, r.m, r.params("R2", "wxpay")))
	if wx.Channel != r.epay.Key() || wx.PayType != "wxpay" || wx.PayAmount != wx.Amount || !slices.Equal(r.prepays(), []string{"wxpay"}) {
		t.Errorf("微信订单：%+v，预下单 %v", wx, r.prepays())
	}
	res := e.PostForm("/mapi.php", r.m.SignMD5(r.params("R3", "qqpay"))).JSON(t)
	if res["msg"] != "暂不支持QQ钱包，请使用支付宝或微信支付付款" {
		t.Errorf("没开放的方式：%v", res)
	}

	// 第三方渠道停用后：带微信的下单失败，留给付款人选的订单里微信标成暂时不可用。
	if res := r.a.Call("PUT", fmt.Sprintf("/web-api/admin/channels/%d", r.epay.ID), map[string]any{"name": "第三方", "enabled": false,
		"config": map[string]any{"api_url": r.upURL, "version": 1, "pid": "1000", "types": []string{"alipay", "wxpay"}}}); res.Status != http.StatusOK {
		t.Fatalf("停用渠道：%d %s", res.Status, res.Body)
	}
	if res := e.PostForm("/mapi.php", r.m.SignMD5(r.params("R4", "wxpay"))).JSON(t); res["msg"] != "支付通道暂时不可用，请联系商户" {
		t.Errorf("渠道停用时下单：%v", res)
	}
	tn, body := r.submit(t, "R5", "")
	if tn == "" {
		t.Fatalf("没带方式的跳转下单：%s", body)
	}
	opts, _ := e.Get("/web-api/cashier/" + tn).JSON(t)["pay_types"].([]any)
	if fmt.Sprint(opts) != "[map[label:支付宝 type:alipay] map[label:微信支付 type:wxpay unavailable:暂时不可用]]" {
		t.Errorf("收银台的支付方式：%v", opts)
	}

	// 商户一种方式都没开：不建单。
	none := e.NewMerchant(&merchant.Input{Name: "没开方式", Enabled: true, Routes: map[string]int64{"alipay": 0}})
	if res := e.PostForm("/mapi.php", none.SignMD5(r.params("R6", "alipay"))).JSON(t); res["msg"] != "支付通道暂时不可用，请联系商户" {
		t.Errorf("没开方式的商户下单：%v", res)
	}
}

func TestCashierChooseOnceAndCancel(t *testing.T) {
	r := newRouted(t)
	e := r.e
	tn, body := r.submit(t, "C1", "")
	if tn == "" {
		t.Fatalf("跳转下单：%s", body)
	}
	o := e.Order(tn)
	if o.Channel != "" || o.PayType != "" || o.PayAmount != o.Amount {
		t.Fatalf("没带方式时先不定渠道、不加尾数：%+v", o)
	}
	v := e.Get("/web-api/cashier/" + tn).JSON(t)
	if types, _ := v["pay_types"].([]any); len(types) != 2 || v["guide"] != nil {
		t.Fatalf("收银台：%v", v)
	}

	// 选支付宝：定下演示渠道并分配尾数，返回付款金额和收款码。
	base := "/web-api/cashier/" + tn
	res, m := postJSON(t, e, base+"/pay", map[string]string{"type": "alipay"})
	o = e.Order(tn)
	if res.Status != http.StatusOK || m["pay_type"] != "alipay" || m["pay_amount"] != epay.FormatCents(o.PayAmount) || m["guide"] == nil || o.Channel != e.ChannelKey() {
		t.Fatalf("选支付宝：%d %v，订单 %+v", res.Status, m, o)
	}
	// 再选一次同样的方式没问题，换微信不行，也不向第三方平台预下单。
	if res, m := postJSON(t, e, base+"/pay", map[string]string{"type": "alipay"}); res.Status != http.StatusOK || m["pay_amount"] != epay.FormatCents(o.PayAmount) {
		t.Errorf("再选支付宝：%d %v", res.Status, m)
	}
	if res, m := postJSON(t, e, base+"/pay", map[string]string{"type": "wxpay"}); res.Status != http.StatusConflict || m["error"] != "已经选了支付宝，不能再换；要换支付方式请取消订单，回到商户重新下单" || len(r.prepays()) != 0 {
		t.Errorf("换微信：%d %v，预下单 %v", res.Status, m, r.prepays())
	}
	if v := e.Get(base).JSON(t); v["pay_types"] != nil || v["guide"] == nil {
		t.Errorf("选定后收银台：%v", v)
	}

	// 取消订单：要带上收银台的请求头；只记一次审计；取消后付款进流水异常。
	if res, _ := postJSON(t, e, base+"/cancel", nil); res.Status != http.StatusForbidden {
		t.Errorf("不带请求头取消：%d", res.Status)
	}
	cancel := func() (*apptest.Response, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, e.URL+base+"/cancel", nil)
		req.Header.Set("X-Tillbox", "1")
		res := e.Do(req)
		var m map[string]any
		_ = json.Unmarshal(res.Body, &m)
		return res, m
	}
	if res, m := cancel(); res.Status != http.StatusOK || m["state"] != store.OrderClosed {
		t.Fatalf("取消：%d %v", res.Status, m)
	}
	if res, m := cancel(); res.Status != http.StatusOK || m["state"] != store.OrderClosed {
		t.Errorf("再取消一次：%d %v", res.Status, m)
	}
	n, err := e.DB().NewSelect().Model((*store.AuditLog)(nil)).Where("action = ?", "order.cancel").Where("actor = ?", "付款人").Where("target = ?", tn).Count(e.Ctx)
	if err != nil || n != 1 {
		t.Errorf("审计记录 %d 条：%v", n, err)
	}
	if v := e.Get(base).JSON(t); v["state"] != store.OrderClosed {
		t.Errorf("取消后收银台：%v", v)
	}
	if res, _ := postJSON(t, e, base+"/pay", map[string]string{"type": "alipay"}); res.Status != http.StatusConflict {
		t.Errorf("取消后选方式：%d", res.Status)
	}
	e.Pay(tn)
	var anomalies int64
	if anomalies, err = e.DB().NewSelect().Model((*store.Anomaly)(nil)).Count(e.Ctx); err != nil || anomalies != 1 {
		t.Errorf("取消后付款应进流水异常：%d %v", anomalies, err)
	}

	// 已支付的订单不能取消。
	paid := create(t, e, r.m, r.params("C2", "alipay"))
	e.Pay(paid)
	req, _ := http.NewRequest(http.MethodPost, e.URL+"/web-api/cashier/"+paid+"/cancel", nil)
	req.Header.Set("X-Tillbox", "1")
	if res := e.Do(req); res.Status != http.StatusConflict {
		t.Errorf("已支付后取消：%d %s", res.Status, res.Body)
	}
}

// 付款人选了第三方易支付的方式：当场预下单，之后同样不能换。
func TestCashierChooseUpstream(t *testing.T) {
	r := newRouted(t)
	e := r.e
	tn, body := r.submit(t, "U1", "")
	if tn == "" {
		t.Fatalf("跳转下单：%s", body)
	}
	base := "/web-api/cashier/" + tn
	res, m := postJSON(t, e, base+"/pay", map[string]string{"type": "wxpay"})
	g, _ := m["guide"].(map[string]any)
	if res.Status != http.StatusOK || g["pay_url"] != "https://up.example.com/pay" || m["pay_amount"] != "5.00" || !slices.Equal(r.prepays(), []string{"wxpay"}) {
		t.Fatalf("选微信：%d %v，预下单 %v", res.Status, m, r.prepays())
	}
	if res, m := postJSON(t, e, base+"/pay", map[string]string{"type": "alipay"}); res.Status != http.StatusConflict || !strings.Contains(fmt.Sprint(m["error"]), "已经选了微信支付") {
		t.Errorf("换支付宝：%d %v", res.Status, m)
	}
	if v := e.Get(base).JSON(t); v["pay_type"] != "wxpay" || fmt.Sprint(v["pay_types"]) != "[map[label:微信支付 type:wxpay]]" {
		t.Errorf("选定后收银台：%v", v)
	}
}
