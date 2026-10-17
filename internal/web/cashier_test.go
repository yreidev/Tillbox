package web_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/epay"
	"tillbox/internal/store"
)

type cashierView struct {
	TradeNo    string `json:"trade_no"`
	Name       string `json:"name"`
	Amount     string `json:"amount"`
	PayAmount  string `json:"pay_amount"`
	State      string `json:"state"`
	ExpiresAt  int64  `json:"expires_at"`
	PaidAt     int64  `json:"paid_at"`
	ServerTime int64  `json:"server_time"`
	SiteName   string `json:"site_name"`
	HasReturn  bool   `json:"has_return"`
	Guide      *struct {
		QRCode       string `json:"qrcode"`
		ManualAmount bool   `json:"manual_amount"`
	} `json:"guide"`
}

func view(t *testing.T, e *apptest.Env, tn string) cashierView {
	t.Helper()
	res := e.Get("/web-api/cashier/" + tn)
	if res.Status != http.StatusOK || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("收银台：%d %v %s", res.Status, res.Header, res.Body)
	}
	var v cashierView
	if err := json.Unmarshal(res.Body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func returnURL(t *testing.T, e *apptest.Env, tn string) string {
	t.Helper()
	u, _ := e.Get("/web-api/cashier/" + tn + "/return").JSON(t)["url"].(string)
	return u
}

func create(t *testing.T, e *apptest.Env, m *apptest.Merchant, p map[string]string) string {
	t.Helper()
	res := e.PostForm("/mapi.php", m.SignMD5(p)).JSON(t)
	tn, _ := res["trade_no"].(string)
	if tn == "" {
		t.Fatalf("下单失败：%v", res)
	}
	return tn
}

func TestCashier(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	tn := create(t, e, m, map[string]string{
		"type": "alipay", "out_trade_no": "C1", "notify_url": e.Hook.URL + "/n", "return_url": "https://shop.example.com/#/result?from=pay",
		"name": "<b>会员</b>", "money": "12.3", "clientip": "1.1.1.1",
	})

	for _, p := range []string{"abc", "123", "9999999999999999999", tn + "0"} {
		if res := e.Get("/web-api/cashier/" + p); res.Status != http.StatusNotFound || !strings.Contains(string(res.Body), "订单不存在") {
			t.Errorf("订单号 %s：%d %s", p, res.Status, res.Body)
		}
	}

	v := view(t, e, tn)
	o := e.Order(tn)
	if v.State != store.OrderPending || v.Amount != "12.30" || v.PayAmount != epay.FormatCents(o.PayAmount) || v.Name != "<b>会员</b>" || v.HasReturn {
		t.Errorf("待付款：%+v", v)
	}
	if v.Guide == nil || v.Guide.QRCode == "" || !v.Guide.ManualAmount || v.ExpiresAt != o.ExpiresAt || v.SiteName == "" {
		t.Errorf("收款指引：%+v", v)
	}
	if d := time.Since(store.Time(v.ServerTime)); d < 0 || d > 5*time.Second {
		t.Errorf("服务器时间：%v", d)
	}
	if returnURL(t, e, tn) != "" {
		t.Error("没付款不该给回跳地址")
	}

	// "我已付款"：只是催查一轮，不改订单。
	res := e.PostForm("/web-api/cashier/"+tn+"/paid", nil)
	if res.Status != http.StatusOK || e.Order(tn).Status != store.OrderPending {
		t.Errorf("我已付款：%d", res.Status)
	}

	e.Pay(tn)
	v = view(t, e, tn)
	if v.State != store.OrderPaid || v.Guide != nil || !v.HasReturn || v.PaidAt == 0 {
		t.Errorf("付款后：%+v", v)
	}
	// 和官方一样把参数直接接在地址末尾（地址里有 ? 用 &）：哈希路由的页面从片段里读参数。
	ru := returnURL(t, e, tn)
	frag, ok := strings.CutPrefix(ru, "https://shop.example.com/#/result?from=pay&")
	if !ok {
		t.Fatalf("回跳地址：%s", ru)
	}
	q, err := url.ParseQuery("from=pay&" + frag)
	if err != nil {
		t.Fatal(err)
	}
	p := epay.Params{}
	for k, vs := range q {
		if k != "from" {
			p[k] = vs[0]
		}
	}
	if q.Get("from") != "pay" || !epay.MD5Verify(p, m.Key) || p["trade_status"] != "TRADE_SUCCESS" {
		t.Errorf("回跳参数：%v", q)
	}

	// 付款超过 5 分钟后不再给回跳。
	if _, err := e.DB().NewUpdate().Model((*store.Order)(nil)).Set("paid_at = ?", store.Ms(time.Now().Add(-6*time.Minute))).Where("trade_no = ?", tn).Exec(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if v = view(t, e, tn); v.HasReturn || returnURL(t, e, tn) != "" {
		t.Error("超过 5 分钟还给回跳")
	}
}

func TestCashierExpiredAndClosed(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	p := func(out string) map[string]string {
		return map[string]string{"type": "alipay", "out_trade_no": out, "notify_url": e.Hook.URL + "/n", "name": "x", "money": "1", "clientip": "1.1.1.1"}
	}
	expired := create(t, e, m, p("E1"))
	if _, err := e.DB().NewUpdate().Model((*store.Order)(nil)).Set("expires_at = ?", store.Now()-1).Where("trade_no = ?", expired).Exec(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if v := view(t, e, expired); v.State != store.OrderExpired || v.Guide != nil {
		t.Errorf("到期订单：%+v", v)
	}
	// 过期后付款人说已付款，照样催查（可能是晚到的付款）。
	if res := e.PostForm("/web-api/cashier/"+expired+"/paid", nil); res.Status != http.StatusOK {
		t.Errorf("过期订单点已付款：%d", res.Status)
	}

	closed := create(t, e, m, p("E2"))
	if _, err := e.App.Orders.Close(e.Ctx, e.Order(closed).ID); err != nil {
		t.Fatal(err)
	}
	if v := view(t, e, closed); v.State != store.OrderClosed || v.Guide != nil {
		t.Errorf("关闭的订单：%+v", v)
	}

	// 付了款但没给 return_url：不回跳。
	noReturn := create(t, e, m, p("E3"))
	e.Pay(noReturn)
	if v := view(t, e, noReturn); v.HasReturn || returnURL(t, e, noReturn) != "" {
		t.Error("没有 return_url 不该回跳")
	}
}

func TestCashierV2Return(t *testing.T) {
	e := apptest.New(t)
	m := e.NewRSAMerchant(false)
	res := e.PostForm("/api/pay/create", m.SignRSA(map[string]string{
		"type": "alipay", "out_trade_no": "R1", "notify_url": e.Hook.URL + "/n", "return_url": e.Hook.URL + "/r",
		"name": "x", "money": "2", "clientip": "1.1.1.1",
	})).JSON(t)
	tn, _ := res["trade_no"].(string)
	if tn == "" {
		t.Fatalf("V2 下单：%v", res)
	}
	e.Pay(tn)
	u, err := url.Parse(returnURL(t, e, tn))
	if err != nil {
		t.Fatal(err)
	}
	p := epay.Params{}
	for k, vs := range u.Query() {
		p[k] = vs[0]
	}
	if p["sign_type"] != "RSA" || p["timestamp"] == "" || !epay.RSAVerify(epay.SignContent(p), p["sign"], e.PlatformKey()) {
		t.Errorf("V2 回跳参数：%v", p)
	}
}

func TestDevPay(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	tn := create(t, e, m, map[string]string{"type": "alipay", "out_trade_no": "D1", "notify_url": e.Hook.URL + "/n", "name": "x", "money": "3", "clientip": "1.1.1.1"})
	post := func(body string) *apptest.Response {
		req, _ := http.NewRequest(http.MethodPost, e.URL+"/web-api/dev/pay", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return e.Do(req)
	}
	for body, status := range map[string]int{
		`{bad`:                         http.StatusBadRequest,
		`{}`:                           http.StatusBadRequest,
		`{"amount":"abc"}`:             http.StatusBadRequest,
		`{"trade_no":"1234567890123"}`: http.StatusNotFound,
	} {
		if res := post(body); res.Status != status {
			t.Errorf("%s：%d %s", body, res.Status, res.Body)
		}
	}
	res := post(`{"trade_no":"` + tn + `"}`)
	if res.Status != http.StatusOK || res.JSON(t)["amount"] != epay.FormatCents(e.Order(tn).PayAmount) {
		t.Fatalf("模拟付款：%d %s", res.Status, res.Body)
	}
	// 模拟付款只记流水并催查，到账靠查账。
	e.Poll()
	if e.Order(tn).Status != store.OrderPaid {
		t.Error("模拟付款后查账应到账")
	}
	if res := post(`{"amount":"9.99","payer":"某人"}`); res.Status != http.StatusOK {
		t.Errorf("按金额模拟付款：%d %s", res.Status, res.Body)
	}
}
