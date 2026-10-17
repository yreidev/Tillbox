package gateway_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/epay"
	"tillbox/internal/merchant"
	"tillbox/internal/store"
)

var tradeNoRe = regexp.MustCompile(`^\d{19}$`)

func orderParams(e *apptest.Env, outTradeNo, money string) map[string]string {
	return map[string]string{
		"type": "alipay", "out_trade_no": outTradeNo, "notify_url": e.Hook.URL + "/notify",
		"return_url": e.Hook.URL + "/return", "name": "测试商品", "money": money, "clientip": "203.0.113.9",
	}
}

func str(t *testing.T, m map[string]any, k string) string {
	t.Helper()
	switch v := m[k].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case nil:
		return "<null>"
	default:
		return fmt.Sprint(v)
	}
}

func wantMsg(t *testing.T, res map[string]any, code, msg string) {
	t.Helper()
	if str(t, res, "code") != code || str(t, res, "msg") != msg {
		t.Errorf("想要 code=%s msg=%q，得到 %v", code, msg, res)
	}
}

func mapi(e *apptest.Env, m *apptest.Merchant, p map[string]string) map[string]any {
	e.T.Helper()
	return e.PostForm("/mapi.php", m.SignMD5(p)).JSON(e.T)
}

func TestMAPIAndQuery(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)

	res := mapi(e, m, orderParams(e, "A001", "10.00"))
	if str(t, res, "code") != "1" || !tradeNoRe.MatchString(str(t, res, "trade_no")) {
		t.Fatalf("下单失败：%v", res)
	}
	tn := str(t, res, "trade_no")
	cashier := e.URL + "/cashier/" + tn
	if res["payurl"] != cashier || res["qrcode"] != cashier {
		t.Errorf("payurl/qrcode 应指向收银台：%v", res)
	}
	if _, ok := res["payurl2"]; ok {
		t.Error("不该返回 payurl2")
	}
	o := e.Order(tn)
	if o.Amount != 1000 || o.PayAmount != 1001 || o.Money != "10.00" || o.ClientIP != "203.0.113.9" || o.Device != "pc" {
		t.Errorf("订单字段不对：%+v", o)
	}

	// 同一订单号、同样参数再下单：复用原订单。
	again := mapi(e, m, orderParams(e, "A001", "10.00"))
	if str(t, again, "trade_no") != tn {
		t.Errorf("重复下单应复用原订单：%v", again)
	}
	// 参数变了：报错。
	wantMsg(t, mapi(e, m, orderParams(e, "A001", "10.01")), "-1", "该订单(A001)支付参数有变化，请更换订单号重新发起支付")

	// 另一个同额订单分到下一个尾数。
	res2 := mapi(e, m, orderParams(e, "A002", "10"))
	if o2 := e.Order(str(t, res2, "trade_no")); o2.PayAmount != 1002 || o2.Money != "10" {
		t.Errorf("第二单应付 10.02，实际 %d，money=%q", o2.PayAmount, o2.Money)
	}

	// 查单（官方 SDK 的 GET 写法，按 trade_no）。
	q := url.Values{"act": {"order"}, "pid": {m.PID()}, "key": {m.Key}, "trade_no": {tn}}
	r := e.Get("/api.php?" + q.Encode()).JSON(t)
	if str(t, r, "code") != "1" || str(t, r, "status") != "0" || str(t, r, "money") != "10.00" || str(t, r, "out_trade_no") != "A001" ||
		str(t, r, "pid") != m.PID() || r["endtime"] != nil || r["api_trade_no"] != nil || str(t, r, "msg") != "succ" {
		t.Errorf("未付款查单结果不对：%v", r)
	}

	e.Pay(tn)
	r = e.Get("/api.php?" + q.Encode()).JSON(t)
	if str(t, r, "status") != "1" || r["endtime"] == nil || r["buyer"] == nil || r["api_trade_no"] == nil {
		t.Errorf("付款后查单结果不对：%v", r)
	}
	if str(t, r, "money") != "10.00" {
		t.Errorf("查单 money 应是订单金额，不是实付金额：%v", r["money"])
	}

	// 已付款后再用同号下单。
	wantMsg(t, mapi(e, m, orderParams(e, "A001", "10.00")), "-1", "该订单(A001)已完成支付，请勿重复发起支付")
}

func TestMAPIErrors(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	good := func() map[string]string { return orderParams(e, "E001", "1.00") }

	if r := e.PostForm("/mapi.php", url.Values{}).JSON(t); str(t, r, "code") != "-4" || r["msg"] != "未传入任何参数" {
		t.Errorf("没参数：%v", r)
	}
	// 只在查询串里给参数也不行：mapi 只读表单。
	if r := e.PostForm("/mapi.php?"+m.SignMD5(good()).Encode(), url.Values{}).JSON(t); str(t, r, "code") != "-4" {
		t.Errorf("mapi 不该读查询串：%v", r)
	}

	cases := []struct {
		name string
		edit func(p map[string]string)
		code string
		msg  string
	}{
		{"商户号非数字", func(p map[string]string) { p["pid"] = "abc" }, "-1", "商户ID不能为空"},
		{"商户不存在", func(p map[string]string) { p["pid"] = "99999" }, "-1", "商户不存在！"},
		{"没订单号", func(p map[string]string) { delete(p, "out_trade_no") }, "-1", "订单号(out_trade_no)不能为空"},
		{"订单号是 0", func(p map[string]string) { p["out_trade_no"] = "0" }, "-1", "订单号(out_trade_no)不能为空"},
		{"没通知地址", func(p map[string]string) { delete(p, "notify_url") }, "-1", "通知地址(notify_url)不能为空"},
		{"没商品名", func(p map[string]string) { delete(p, "name") }, "-1", "商品名称(name)不能为空"},
		{"没金额", func(p map[string]string) { delete(p, "money") }, "-1", "金额(money)不能为空"},
		{"没支付方式", func(p map[string]string) { delete(p, "type") }, "-1", "支付方式(type)不能为空"},
		{"没 IP", func(p map[string]string) { delete(p, "clientip") }, "-1", "用户IP地址(clientip)不能为空"},
		{"订单号格式", func(p map[string]string) { p["out_trade_no"] = "a b" }, "-1", "订单号(out_trade_no)格式不正确"},
		{"金额负数", func(p map[string]string) { p["money"] = "-1" }, "-1", "金额不合法"},
		{"金额科学计数", func(p map[string]string) { p["money"] = "1e2" }, "-1", "金额不合法"},
		{"金额太大", func(p map[string]string) { p["money"] = "100000000" }, "-1", "金额不合法"},
		{"IP 不合法", func(p map[string]string) { p["clientip"] = "1.2.3" }, "-1", "用户IP地址不合法"},
		{"通知地址格式", func(p map[string]string) { p["notify_url"] = "ftp://x" }, "-1", "通知地址(notify_url)格式不正确"},
		{"微信", func(p map[string]string) { p["type"] = "wxpay" }, "-1", "暂不支持微信支付，请使用支付宝付款"},
		{"未知方式", func(p map[string]string) { p["type"] = "foo" }, "-1", "暂不支持该支付方式（foo），请使用支付宝付款"},
		{"jsapi", func(p map[string]string) { p["method"] = "jsapi" }, "-1", "暂不支持jsapi支付方式，请使用页面跳转或二维码支付"},
		{"订单号太长", func(p map[string]string) { p["out_trade_no"] = strings.Repeat("a", 65) }, "-1", "订单号(out_trade_no)不能超过64个字符"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := good()
			c.edit(p)
			wantMsg(t, mapi(e, m, p), c.code, c.msg)
		})
	}

	t.Run("签名错", func(t *testing.T) {
		v := m.SignMD5(good())
		v.Set("sign", strings.Repeat("0", 32))
		wantMsg(t, e.PostForm("/mapi.php", v).JSON(t), "-3", "MD5签名校验失败")
	})
	t.Run("没签名", func(t *testing.T) {
		v := m.SignMD5(good())
		v.Del("sign")
		wantMsg(t, e.PostForm("/mapi.php", v).JSON(t), "-3", "缺少签名参数")
	})
	t.Run("不写 sign_type 也按 MD5", func(t *testing.T) {
		v := m.SignMD5(orderParams(e, "E900", "1.00"))
		v.Del("sign_type")
		if r := e.PostForm("/mapi.php", v).JSON(t); str(t, r, "code") != "1" {
			t.Errorf("%v", r)
		}
	})
	t.Run("签名大写不行", func(t *testing.T) {
		v := m.SignMD5(good())
		v.Set("sign", strings.ToUpper(v.Get("sign")))
		wantMsg(t, e.PostForm("/mapi.php", v).JSON(t), "-3", "MD5签名校验失败")
	})
	t.Run("商户停用", func(t *testing.T) {
		off := e.NewMerchant(&merchant.Input{Name: "停用", Enabled: false})
		wantMsg(t, mapi(e, off, good()), "-1", "商户已被封禁，无法支付！")
	})
	t.Run("域名白名单", func(t *testing.T) {
		wl := e.NewMerchant(&merchant.Input{Name: "白名单", Enabled: true, Domains: "*.example.com\nshop.test"})
		p := good()
		wantMsg(t, mapi(e, wl, p), "-1", "该域名不可发起支付，原因：域名没过白，请前往支付平台授权支付域名")
		p["notify_url"], p["return_url"] = "https://a.example.com/n", "https://shop.test/r"
		if r := mapi(e, wl, p); str(t, r, "code") != "1" {
			t.Errorf("白名单内的域名应能下单：%v", r)
		}
	})
	t.Run("只收 RSA 的商户用 MD5", func(t *testing.T) {
		rm := e.NewRSAMerchant(true)
		wantMsg(t, mapi(e, rm, good()), "-3", "该商户只能使用RSA签名类型")
	})
}

func TestSubmit(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)

	p := orderParams(e, "S001", "5")
	delete(p, "clientip")
	// GET 跳转（弹窗或链接跳转）。
	res := e.Get("/submit.php?" + m.SignMD5(p).Encode())
	loc := res.Header.Get("Location")
	if res.Status != http.StatusSeeOther || !strings.HasPrefix(loc, e.URL+"/cashier/") {
		t.Fatalf("应 303 到收银台：%d %s", res.Status, loc)
	}
	o := e.Order(strings.TrimPrefix(loc, e.URL+"/cashier/"))
	if o.ClientIP != "127.0.0.1" || o.Version != 1 {
		t.Errorf("跳转下单应记录请求方 IP：%+v", o)
	}

	// 浏览器 POST 表单。
	p2 := orderParams(e, "S002", "5")
	p2["device"] = "pc"
	res = e.PostForm("/submit.php", m.SignMD5(p2))
	if res.Status != http.StatusSeeOther {
		t.Fatalf("POST 跳转下单失败：%d %s", res.Status, res.Body)
	}

	// 查询串有 pid 时整组用查询串，表单被忽略。
	res = e.PostForm("/submit.php?pid="+m.PID(), m.SignMD5(orderParams(e, "S003", "5")))
	if res.Status != http.StatusOK || !bytes.Contains(res.Body, []byte("站点提示信息")) {
		t.Errorf("查询串只有 pid 时应报错页：%d", res.Status)
	}

	// 没 return_url。
	p3 := orderParams(e, "S004", "5")
	delete(p3, "return_url")
	res = e.Get("/submit.php?" + m.SignMD5(p3).Encode())
	if !bytes.Contains(res.Body, []byte("回调地址(return_url)不能为空")) || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Errorf("应返回 HTML 提示页：%s", res.Body)
	}
	// 提示页要转义。
	p4 := orderParams(e, "S005", "5")
	p4["type"] = "<b>x</b>"
	res = e.Get("/submit.php?" + m.SignMD5(p4).Encode())
	if bytes.Contains(res.Body, []byte("<b>x</b>")) {
		t.Error("提示页没有转义")
	}
	// 不传 type 按支付宝处理。
	p5 := orderParams(e, "S006", "5")
	delete(p5, "type")
	if res = e.Get("/submit.php?" + m.SignMD5(p5).Encode()); res.Status != http.StatusSeeOther {
		t.Errorf("不传 type 应能下单：%d %s", res.Status, res.Body)
	}

	if res = e.Get("/submit.php"); string(res.Body) != "你还未配置支付接口商户！" {
		t.Errorf("没 pid：%s", res.Body)
	}
}

func TestV1OtherActs(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	tn := str(t, mapi(e, m, orderParams(e, "Q001", "8.80")), "trade_no")
	mapi(e, m, orderParams(e, "Q002", "1"))
	e.Pay(tn)
	auth := func(act string) url.Values {
		return url.Values{"act": {act}, "pid": {m.PID()}, "key": {m.Key}}
	}

	r := e.Get("/api.php?" + auth("query").Encode()).JSON(t)
	if str(t, r, "code") != "1" || str(t, r, "pid") != m.PID() || r["key"] != m.Key || str(t, r, "active") != "1" ||
		r["money"] != "0.00" || str(t, r, "orders") != "2" || str(t, r, "orders_today") != "1" || str(t, r, "orders_lastday") != "0" {
		t.Errorf("act=query：%v", r)
	}

	r = e.Get("/api.php?" + auth("orders").Encode()).JSON(t)
	if str(t, r, "code") != "1" || r["msg"] != "查询订单记录成功！" || str(t, r, "count") != "2" {
		t.Errorf("act=orders：%v", r)
	}
	v := auth("orders")
	v.Set("status", "1")
	if r = e.Get("/api.php?" + v.Encode()).JSON(t); str(t, r, "count") != "1" {
		t.Errorf("按状态筛选：%v", r)
	}

	r = e.Get("/api.php?" + auth("settle").Encode()).JSON(t)
	if str(t, r, "code") != "1" || r["msg"] != "查询结算记录成功！" || len(r["data"].([]any)) != 0 {
		t.Errorf("act=settle：%v", r)
	}

	// 退款：已付款的说通道不支持；未付款的说状态不支持；没订单号、金额错、订单不存在。
	rf := auth("refund")
	rf.Set("trade_no", tn)
	rf.Set("money", "8.80")
	wantMsg(t, e.PostForm("/api.php?act=refund", rf).JSON(t), "-1", "当前支付通道不支持API退款")
	rf.Set("trade_no", "")
	rf.Set("out_trade_no", "Q002")
	wantMsg(t, e.PostForm("/api.php?act=refund", rf).JSON(t), "-1", "该订单状态不支持退款！")
	rf.Set("out_trade_no", "NOPE")
	wantMsg(t, e.PostForm("/api.php?act=refund", rf).JSON(t), "-1", "当前订单不存在！")
	rf.Set("money", "abc")
	wantMsg(t, e.PostForm("/api.php?act=refund", rf).JSON(t), "-1", "金额输入错误")
	rf.Set("money", "1")
	rf.Del("out_trade_no")
	wantMsg(t, e.PostForm("/api.php?act=refund", rf).JSON(t), "-4", "订单号不能为空")

	bad := auth("order")
	bad.Set("key", "x")
	bad.Set("trade_no", tn)
	wantMsg(t, e.Get("/api.php?"+bad.Encode()).JSON(t), "-3", "商户密钥错误")
	bad.Set("pid", "424242")
	wantMsg(t, e.Get("/api.php?"+bad.Encode()).JSON(t), "-3", "商户ID不存在")
	nf := auth("order")
	nf.Set("out_trade_no", "NOPE")
	wantMsg(t, e.Get("/api.php?"+nf.Encode()).JSON(t), "-1", "订单号不存在")
	wantMsg(t, e.Get("/api.php?"+auth("order").Encode()).JSON(t), "-4", "订单号不能为空")
	wantMsg(t, e.Get("/api.php").JSON(t), "-5", "No Act!")
	wantMsg(t, e.Get("/api.php?act=nope").JSON(t), "-5", "No Act!")
}

func TestNotifyV1(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	p := orderParams(e, "N001", "10")
	p["param"] = "uid=7&from=test"
	p["notify_url"] = e.Hook.URL + "/notify?site=1"
	tn := str(t, mapi(e, m, p), "trade_no")
	e.Pay(tn)
	e.Deliver()

	got := e.Hook.Got()
	if len(got) != 1 || got[0].Method != http.MethodGet || got[0].Path != "/notify" {
		t.Fatalf("应收到一次 GET 通知：%+v", got)
	}
	q := got[0].Params()
	if q["site"] != "1" {
		t.Error("通知地址里原有的参数丢了")
	}
	delete(q, "site")
	// 商户验签时会带上自己地址里的参数，所以这里先去掉再验（和官方 SDK 一样会失败，见规格）。
	if !epay.MD5Verify(q, m.Key) {
		t.Errorf("V1 通知验签失败：%v", q)
	}
	want := map[string]string{
		"pid": m.PID(), "trade_no": tn, "out_trade_no": "N001", "type": "alipay", "name": "测试商品",
		"money": "10", "trade_status": "TRADE_SUCCESS", "param": "uid=7&from=test", "sign_type": "MD5",
	}
	for k, v := range want {
		if q[k] != v {
			t.Errorf("通知字段 %s = %q，应为 %q", k, q[k], v)
		}
	}
	for _, k := range []string{"timestamp", "api_trade_no", "buyer"} {
		if _, ok := q[k]; ok {
			t.Errorf("V1 通知不该带 %s", k)
		}
	}
	n := notification(t, e, tn)
	if n.Status != store.NotifySuccess || n.Attempts != 1 {
		t.Errorf("通知状态：%+v", n)
	}

	// 回跳地址：付款后 5 分钟内可拿。
	r := e.Get("/web-api/cashier/" + tn + "/return").JSON(t)
	u, err := url.Parse(r["url"].(string))
	if err != nil || !strings.HasPrefix(r["url"].(string), e.Hook.URL+"/return?") {
		t.Fatalf("回跳地址：%v", r)
	}
	rp := epay.Params{}
	for k, v := range u.Query() {
		rp[k] = v[0]
	}
	if !epay.MD5Verify(rp, m.Key) || rp["money"] != "10" {
		t.Errorf("回跳参数验签失败：%v", rp)
	}
}

func notification(t *testing.T, e *apptest.Env, tradeNo string) *store.Notification {
	t.Helper()
	o := e.Order(tradeNo)
	var n store.Notification
	if err := e.DB().NewSelect().Model(&n).Where("order_id = ?", o.ID).Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	return &n
}

// v2 发一个 V2 请求并校验响应签名（code=0 时官方 SDK 会验签）。
func v2(e *apptest.Env, m *apptest.Merchant, path string, p map[string]string) map[string]any {
	e.T.Helper()
	res := e.PostForm("/api/"+path, m.SignRSA(p)).JSON(e.T)
	if _, signed := res["sign"]; signed {
		verifyV2(e, res)
	}
	return res
}

func verifyV2(e *apptest.Env, res map[string]any) {
	e.T.Helper()
	vals := map[string]any{}
	for k, v := range res {
		if n, ok := v.(json.Number); ok {
			i, err := n.Int64()
			if err != nil {
				e.T.Fatalf("响应里出现了非整数的数字 %s=%v", k, v)
			}
			v = i
		}
		vals[k] = v
	}
	if res["sign_type"] != "RSA" || !epay.RSAVerify(epay.SignContent(epay.SignValues(vals)), res["sign"].(string), e.PlatformKey()) {
		e.T.Errorf("V2 响应验签失败：%v", res)
	}
	ts, _ := strconv.ParseInt(res["timestamp"].(string), 10, 64)
	if d := time.Now().Unix() - ts; d > 5 || d < -5 {
		e.T.Errorf("响应时间戳不对：%v", res["timestamp"])
	}
}

func TestV2(t *testing.T) {
	e := apptest.New(t)
	m := e.NewRSAMerchant(false)

	res := v2(e, m, "pay/create", orderParams(e, "V001", "29.9"))
	tn := str(t, res, "trade_no")
	if str(t, res, "code") != "0" || res["pay_type"] != "qrcode" || res["pay_info"] != e.URL+"/cashier/"+tn {
		t.Fatalf("V2 下单：%v", res)
	}
	if e.Order(tn).Version != 2 {
		t.Error("V2 下单的订单版本应为 2")
	}
	p := orderParams(e, "V002", "1")
	p["device"] = "mobile"
	if res = v2(e, m, "pay/create", p); res["pay_type"] != "jump" {
		t.Errorf("手机端应返回 jump：%v", res)
	}
	p = orderParams(e, "V003", "1")
	p["method"] = "jump"
	if res = v2(e, m, "pay/create", p); res["pay_type"] != "jump" {
		t.Errorf("method=jump 应返回 jump：%v", res)
	}
	p = orderParams(e, "V004", "1")
	p["method"] = "scan"
	if res = v2(e, m, "pay/create", p); res["sign"] != nil || str(t, res, "code") != "-1" {
		t.Errorf("method=scan 应报错且不签名：%v", res)
	}

	// 查单：签名，去掉空字段。
	res = v2(e, m, "pay/query", map[string]string{"out_trade_no": "V001"})
	if str(t, res, "code") != "0" || str(t, res, "status") != "0" || res["money"] != "29.90" || res["clientip"] != "203.0.113.9" {
		t.Errorf("V2 查单：%v", res)
	}
	for _, k := range []string{"endtime", "api_trade_no", "buyer", "param"} {
		if _, ok := res[k]; ok {
			t.Errorf("未付款时不该有 %s", k)
		}
	}
	// 通过 /api.php?s= 访问等同 /api/。
	res = e.PostForm("/api.php?s=pay/query", m.SignRSA(map[string]string{"trade_no": tn})).JSON(t)
	if str(t, res, "code") != "0" {
		t.Errorf("api.php?s=：%v", res)
	}

	e.Pay(tn)
	e.Deliver()
	res = v2(e, m, "pay/query", map[string]string{"trade_no": tn})
	if str(t, res, "status") != "1" || res["endtime"] == nil || res["buyer"] == nil {
		t.Errorf("付款后 V2 查单：%v", res)
	}

	// V2 通知：平台 RSA 签名，带时间戳、api_trade_no、buyer。
	got := e.Hook.Got()
	if len(got) != 1 {
		t.Fatalf("应收到一次通知：%d", len(got))
	}
	q := got[0].Params()
	if q["sign_type"] != "RSA" || q["timestamp"] == "" || q["api_trade_no"] == "" || q["buyer"] == "" || q["money"] != "29.9" {
		t.Errorf("V2 通知字段：%v", q)
	}
	if !epay.RSAVerify(epay.SignContent(q), q["sign"], e.PlatformKey()) {
		t.Error("V2 通知验签失败")
	}

	// 退款和退款查询。
	wantMsg(t, v2(e, m, "pay/refund", map[string]string{"trade_no": tn, "money": "1"}), "-1", "当前支付通道不支持API退款")
	if res = v2(e, m, "pay/refund", map[string]string{"trade_no": "1234567890123456789", "money": "1"}); res["sign"] == nil {
		t.Error("按 trade_no 查不到时官方返回带签名的结果")
	}
	wantMsg(t, v2(e, m, "pay/refund", map[string]string{"out_trade_no": "NOPE", "money": "1"}), "-1", "当前订单不存在！")
	wantMsg(t, v2(e, m, "pay/refund", map[string]string{"trade_no": tn, "money": "x"}), "-1", "金额输入错误")
	wantMsg(t, v2(e, m, "pay/refundquery", map[string]string{}), "-1", "商户退款单号不能为空")
	wantMsg(t, v2(e, m, "pay/refundquery", map[string]string{"out_refund_no": "R1"}), "-1", "退款记录不存在")

	// 关单：未付款的能关，再关也算成功；已付款的不能关。
	tn2 := str(t, v2(e, m, "pay/create", orderParams(e, "V010", "3")), "trade_no")
	wantMsg(t, v2(e, m, "pay/close", map[string]string{"trade_no": tn2}), "0", "订单关闭成功")
	wantMsg(t, v2(e, m, "pay/close", map[string]string{"trade_no": tn2}), "0", "订单关闭成功")
	if e.Order(tn2).Status != store.OrderClosed {
		t.Error("订单没关上")
	}
	wantMsg(t, v2(e, m, "pay/close", map[string]string{"trade_no": tn}), "-1", "该订单状态不支持关闭！")
	// 关掉的订单号不能再下单。
	wantMsg(t, v2(e, m, "pay/create", orderParams(e, "V010", "3")), "-1", "该订单(V010)已失效，请更换订单号重新发起支付")

	// 商户信息和订单列表。
	res = v2(e, m, "merchant/info", nil)
	if str(t, res, "code") != "0" || str(t, res, "pid") != m.PID() || str(t, res, "order_num_today") != "1" || res["order_money_today"] != "29.9" {
		t.Errorf("merchant/info：%v", res)
	}
	res = v2(e, m, "merchant/orders", map[string]string{"limit": "2"})
	if str(t, res, "code") != "0" || len(res["data"].([]any)) != 2 {
		t.Errorf("merchant/orders：%v", res)
	}

	// 路由。
	wantMsg(t, v2(e, m, "transfer/submit", nil), "-5", "接口方法不存在")
	wantMsg(t, e.PostForm("/api/pay", url.Values{}).JSON(t), "-5", "URL Error!")
	wantMsg(t, e.PostForm("/api/pay/query", url.Values{}).JSON(t), "-4", "未传入任何参数")
}

func TestV2Errors(t *testing.T) {
	e := apptest.New(t)
	m := e.NewRSAMerchant(true)

	p := m.SignRSA(orderParams(e, "X001", "1"))
	p.Del("timestamp")
	wantMsg(t, e.PostForm("/api/pay/create", p).JSON(t), "-3", "时间戳(timestamp)字段不能为空")

	old := orderParams(e, "X001", "1")
	old["timestamp"] = strconv.FormatInt(time.Now().Add(-6*time.Minute).Unix(), 10)
	wantMsg(t, e.PostForm("/api/pay/create", m.SignRSA(old)).JSON(t), "-3", "时间戳字段不正确，请检查服务器时间")

	p = m.SignRSA(orderParams(e, "X001", "1"))
	p.Set("name", "改过的")
	wantMsg(t, e.PostForm("/api/pay/create", p).JSON(t), "-3", "RSA签名校验失败")

	wantMsg(t, e.PostForm("/api/pay/query", m.SignMD5(map[string]string{"trade_no": "1", "timestamp": strconv.FormatInt(time.Now().Unix(), 10)})).JSON(t),
		"-3", "该商户只能使用RSA签名类型")

	// 没配 RSA 公钥的商户用 RSA 签名。
	plain := e.NewMerchant(nil)
	plain.Priv = m.Priv
	wantMsg(t, e.PostForm("/api/pay/query", plain.SignRSA(map[string]string{"trade_no": "1"})).JSON(t), "-3", "签名校验失败，商户公钥错误")

	// V2 也能用 MD5（商户没限定只收 RSA 时），响应仍是平台 RSA 签名。
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	q := orderParams(e, "X002", "1")
	q["timestamp"] = ts
	res := e.PostForm("/api/pay/create", plain.SignMD5(q)).JSON(t)
	if str(t, res, "code") != "0" {
		t.Fatalf("V2 MD5 下单：%v", res)
	}
	verifyV2(e, res)
}

// 常见调用方的请求形态：签名都只跳过空串；有的带空的 sign 参数再签名，有的把 act 放在表单里，有的按字符串比较金额。
func TestCallers(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)

	t.Run("empty-sign-form", func(t *testing.T) {
		// 参数里带 sign:"" 再签名，然后浏览器 POST 表单到 /submit.php。
		p := map[string]string{
			"pid": m.PID(), "type": "alipay", "out_trade_no": "T1700000000", "notify_url": e.Hook.URL + "/epay/notify",
			"name": "充值 7.30", "money": "7.30", "device": "pc", "sign_type": "MD5", "return_url": e.Hook.URL + "/done", "sign": "",
		}
		res := e.PostForm("/submit.php", m.SignMD5(p))
		if res.Status != http.StatusSeeOther {
			t.Fatalf("%d %s", res.Status, res.Body)
		}
	})

	t.Run("mapi-form-act", func(t *testing.T) {
		p := map[string]string{
			"pid": m.PID(), "type": "alipay", "out_trade_no": "M20261015_0001", "notify_url": e.Hook.URL + "/epay/notify",
			"return_url": e.Hook.URL + "/done", "name": "Balance Recharge", "money": "20.00", "clientip": "198.51.100.4",
			"cid": "12", "device": "mobile",
		}
		res := mapi(e, m, p)
		if str(t, res, "code") != "1" || res["payurl"] == "" || res["qrcode"] == "" {
			t.Fatalf("%v", res)
		}
		// 查单：act=order 放在 POST 表单里。
		q := url.Values{"act": {"order"}, "pid": {m.PID()}, "key": {m.Key}, "out_trade_no": {"M20261015_0001"}}
		r := e.PostForm("/api.php", q).JSON(t)
		if str(t, r, "code") != "1" || str(t, r, "status") != "0" || r["money"] != "20.00" {
			t.Errorf("表单里放 act 查单：%v", r)
		}
		// 退款：先用 out_trade_no。
		rf := url.Values{"pid": {m.PID()}, "key": {m.Key}, "money": {"20.00"}, "out_trade_no": {"M20261015_0001"}}
		wantMsg(t, e.PostForm("/api.php?act=refund", rf).JSON(t), "-1", "该订单状态不支持退款！")
	})

	t.Run("strict-money", func(t *testing.T) {
		p := map[string]string{
			"pid": m.PID(), "type": "alipay", "out_trade_no": "R2026101500001", "notify_url": e.Hook.URL + "/epay/notify",
			"return_url": e.Hook.URL + "/done", "name": "账户充值", "money": "10.00",
		}
		res := e.Get("/submit.php?" + m.SignMD5(p).Encode())
		if res.Status != http.StatusSeeOther {
			t.Fatalf("%d %s", res.Status, res.Body)
		}
		tn := strings.TrimPrefix(res.Header.Get("Location"), e.URL+"/cashier/")
		e.Pay(tn)
		e.Deliver()
		var q epay.Params
		for _, g := range e.Hook.Got() {
			if g.Query.Get("out_trade_no") == "R2026101500001" {
				q = g.Params()
			}
		}
		// 有的调用方按字符串比较金额，必须原样是 "10.00"。
		if q == nil || q["money"] != "10.00" || !epay.MD5Verify(q, m.Key) {
			t.Errorf("调用方收到的通知：%v", q)
		}
	})
}

func TestMultipartForm(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range m.SignMD5(orderParams(e, "M001", "2")) {
		_ = w.WriteField(k, v[0])
	}
	_ = w.Close()
	req, _ := http.NewRequest(http.MethodPost, e.URL+"/mapi.php", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if r := e.Do(req).JSON(t); str(t, r, "code") != "1" {
		t.Errorf("multipart 表单下单：%v", r)
	}
}

func TestExpiredOrderNumber(t *testing.T) {
	e := apptest.New(t)
	m := e.NewMerchant(nil)
	tn := str(t, mapi(e, m, orderParams(e, "T001", "3")), "trade_no")
	// 拨到过期之后。
	_, err := e.DB().NewUpdate().Model((*store.Order)(nil)).Set("expires_at = ?", store.Ms(time.Now().Add(-time.Second))).
		Where("trade_no = ?", tn).Exec(e.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantMsg(t, mapi(e, m, orderParams(e, "T001", "3")), "-1", "该订单(T001)已失效，请更换订单号重新发起支付")
}
