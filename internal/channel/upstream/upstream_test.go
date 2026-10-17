package upstream

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/secret"
	"tillbox/internal/store"
)

func box(t *testing.T) *secret.Box {
	t.Helper()
	b, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func merge(t *testing.T, b *secret.Box, cur string, in Input, enabled bool) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(in)
	return Merge(b, cur, raw, enabled)
}

func TestMerge(t *testing.T) {
	b := box(t)
	priv, pub, _ := epay.GenerateKeyPair()
	_, platform, _ := epay.GenerateKeyPair()
	ok := Input{APIURL: "https://pay.example.com/", Version: 1, PID: "1001", Key: "k123", Types: []string{"alipay", "wxpay", "alipay"}}
	for _, c := range []struct {
		edit    func(*Input)
		enabled bool
		want    string
	}{
		{func(x *Input) { x.APIURL = "http://pay.example.com" }, false, "https://"},
		{func(x *Input) { x.APIURL = "https://pay.example.com/?a=1" }, false, "根地址"},
		{func(x *Input) { x.APIURL = "ftp://x" }, false, "https://"},
		{func(x *Input) { x.Version = 3 }, false, "V1"},
		{func(x *Input) { x.PID = "1 2" }, false, "商户号"},
		{func(x *Input) { x.Key = "a b" }, false, "空格"},
		{func(x *Input) { x.Types = []string{"cash"} }, false, "不认识的支付方式"},
		{func(x *Input) { x.PlatformPublicKey = "xyz" }, false, "平台公钥"},
		{func(x *Input) { x.PrivateKey = "bad" }, false, "商户私钥"},
		{func(x *Input) { x.Types = nil }, true, "至少勾选一种"},
		{func(x *Input) { x.Version = 2 }, true, "V2 要有商户私钥和平台公钥"},
		{func(x *Input) { x.Key = "" }, true, "V1 要填商户密钥"},
	} {
		in := ok
		c.edit(&in)
		_, err := merge(t, b, "", in, c.enabled)
		var inv *channel.InvalidError
		if !errors.As(err, &inv) || !strings.Contains(inv.Msg, c.want) {
			t.Errorf("想要 %q，得到 %v", c.want, err)
		}
	}
	// 本机地址可以用 HTTP；末尾的 /mapi.php 会被去掉；支付方式去重保序；密钥加密。
	in := ok
	in.APIURL = "http://127.0.0.1:8080/mapi.php"
	raw, err := merge(t, b, "", in, true)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := ParseConfig(raw)
	if c.APIURL != "http://127.0.0.1:8080" || strings.Join(c.Types, ",") != "alipay,wxpay" || c.Key == "" || c.Key == "k123" {
		t.Fatalf("合并结果：%+v", c)
	}
	if k, _ := b.Open(c.Key); k != "k123" {
		t.Error("商户密钥应加密保存")
	}
	// 不填密钥保留原值；换了地址必须重填。
	in.Key = ""
	if _, err := merge(t, b, raw, in, true); err != nil {
		t.Errorf("没换地址时保留密钥：%v", err)
	}
	in.APIURL = "https://other.example.com"
	if _, err := merge(t, b, raw, in, true); err == nil || !strings.Contains(err.Error(), "重新填写商户密钥") {
		t.Errorf("换地址要重填密钥：%v", err)
	}
	// V2：导入私钥，公钥按私钥算出来。
	v2 := Input{APIURL: "https://pay.example.com", Version: 2, PID: "1001", PrivateKey: priv, PlatformPublicKey: platform, Types: []string{"alipay"}}
	raw, err = merge(t, b, "", v2, true)
	if err != nil {
		t.Fatal(err)
	}
	if v := ViewOf(raw); v.PublicKey != pub || !v.HasPrivateKey || v.HasKey || v.Version != 2 {
		t.Errorf("V2 配置：%+v", v)
	}
	if strings.Contains(raw, priv[:40]) {
		t.Error("私钥应加密保存")
	}
	// 生成密钥对。
	raw2, pub2, err := Keypair(b, raw)
	if err != nil || pub2 == "" || ViewOf(raw2).PublicKey != pub2 || pub2 == pub {
		t.Errorf("生成密钥对：%v", err)
	}
}

// stub 是一个假的上游易支付，按测试设定回复。
type stub struct {
	t        *testing.T
	srv      *httptest.Server
	key      string          // V1 商户密钥
	merchant *rsa.PublicKey  // V2 商户公钥（验请求）
	platform *rsa.PrivateKey // V2 平台私钥（签响应）
	reply    func(path string, p epay.Params) map[string]any
	got      []epay.Params
}

func newStub(t *testing.T) *stub {
	s := &stub{t: t}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p := epay.Params{}
		for k, v := range r.Form {
			p[k] = v[0]
		}
		s.got = append(s.got, p)
		if strings.HasPrefix(r.URL.Path, "/redirect/") {
			http.Redirect(w, r, "https://example.com", http.StatusFound)
			return
		}
		res := s.reply(r.URL.Path, p)
		if res == nil {
			_, _ = w.Write([]byte("<html>维护中</html>"))
			return
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// sign 用平台私钥给 V2 响应签名。
func (s *stub) sign(m map[string]any) map[string]any {
	if _, ok := m["timestamp"]; !ok {
		m["timestamp"] = strconv.FormatInt(time.Now().Unix(), 10)
	}
	sig, err := epay.RSASign(epay.SignContent(epay.DecodedValues(roundTrip(m))), s.platform)
	if err != nil {
		s.t.Fatal(err)
	}
	m["sign"], m["sign_type"] = sig, "RSA"
	return m
}

// roundTrip 模拟 JSON 编码再解码（数字保留原文），和客户端看到的值一样。
func roundTrip(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var out map[string]any
	_ = dec.Decode(&out)
	return out
}

func newChannel(t *testing.T, s *stub, version int) *Channel {
	t.Helper()
	b := box(t)
	in := Input{APIURL: s.srv.URL, Version: version, PID: "1001", Types: []string{"alipay", "wxpay"}}
	if version == 1 {
		s.key = "k123"
		in.Key = s.key
	} else {
		mpriv, mpub, _ := epay.GenerateKeyPair()
		ppriv, ppub, _ := epay.GenerateKeyPair()
		s.merchant, _ = epay.ParsePublicKey(mpub)
		s.platform, _ = epay.ParsePrivateKey(ppriv)
		in.PrivateKey, in.PlatformPublicKey = mpriv, ppub
	}
	raw, err := merge(t, b, "", in, true)
	if err != nil {
		t.Fatal(err)
	}
	c := New(b)
	c.Configure(store.Channel{ID: 7, Type: store.ChannelEpay, Name: "上游", Enabled: true, Config: raw})
	return c
}

var testOrder = &store.Order{TradeNo: "2026101700000012345", PayType: "wxpay", Name: "商品", Amount: 1050, ClientIP: "1.2.3.4", Device: "mobile"}

func TestPrepayV1(t *testing.T) {
	s := newStub(t)
	c := newChannel(t, s, 1)
	if err := c.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		res      map[string]any
		infoType string
		info     string
	}{
		{map[string]any{"code": 1, "trade_no": "U1", "qrcode": "weixin://wxpay/x", "payurl": "https://up/p"}, store.InfoQRCode, "weixin://wxpay/x"},
		{map[string]any{"code": "1", "trade_no": "U1", "payurl": "https://up/p"}, store.InfoJump, "https://up/p"},
		{map[string]any{"code": 1, "trade_no": "U1", "urlscheme": "weixin://dl/x"}, store.InfoURLScheme, "weixin://dl/x"},
		{map[string]any{"code": 1, "trade_no": "U1", "html": "<form></form>"}, store.InfoSubmit, ""},
	} {
		s.reply = func(string, epay.Params) map[string]any { return x.res }
		pay, err := c.Prepay(context.Background(), testOrder, "https://till.example.com")
		if err != nil || pay.TradeNo != "U1" || pay.InfoType != x.infoType || pay.Info != x.info {
			t.Errorf("%v → %+v %v", x.res, pay, err)
		}
	}
	p := s.got[0]
	if !epay.MD5Verify(p, s.key) || p["pid"] != "1001" || p["out_trade_no"] != testOrder.TradeNo || p["money"] != "10.50" || p["type"] != "wxpay" ||
		p["notify_url"] != "https://till.example.com/web-api/channels/7/notify" || p["return_url"] != "https://till.example.com/cashier/"+testOrder.TradeNo ||
		p["device"] != "mobile" || p["clientip"] != "1.2.3.4" {
		t.Errorf("V1 下单参数：%v", p)
	}
	// 错误：上游业务错误、不是 JSON、跳转链接不是网址。
	s.reply = func(string, epay.Params) map[string]any { return map[string]any{"code": -2, "msg": "通道维护"} }
	var ue *Error
	if _, err := c.Prepay(context.Background(), testOrder, "https://x"); !errors.As(err, &ue) || ue.Code != "-2" || ue.Msg != "通道维护" {
		t.Errorf("上游业务错误：%v", err)
	}
	s.reply = func(string, epay.Params) map[string]any { return nil }
	if _, err := c.Prepay(context.Background(), testOrder, "https://x"); err == nil || !strings.Contains(err.Error(), "不是 JSON") {
		t.Errorf("不是 JSON：%v", err)
	}
	s.reply = func(string, epay.Params) map[string]any {
		return map[string]any{"code": 1, "trade_no": "U1", "payurl": "javascript:alert(1)"}
	}
	if _, err := c.Prepay(context.Background(), testOrder, "https://x"); err == nil {
		t.Error("跳转链接必须是网址")
	}
	for link, valid := range map[string]bool{"alipays://platformapi/startapp?appId=1": true, "weixin://wxpay/bizpayurl?pr=1": true, "JavaScript:alert(1)": false, "data:text/html,1": false, "no-scheme": false} {
		s.reply = func(string, epay.Params) map[string]any {
			return map[string]any{"code": 1, "trade_no": "U1", "urlscheme": link}
		}
		if _, err := c.Prepay(context.Background(), testOrder, "https://x"); (err == nil) != valid {
			t.Errorf("唤起链接 %s：%v", link, err)
		}
	}
	// 不跟随重定向。
	cfg, _ := c.Config()
	cfg.APIURL = s.srv.URL + "/redirect"
	raw, _ := json.Marshal(cfg)
	c.Configure(store.Channel{ID: 7, Type: store.ChannelEpay, Name: "上游", Enabled: true, Config: string(raw)})
	if _, err := c.Prepay(context.Background(), testOrder, "https://x"); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Errorf("重定向：%v", err)
	}
}

func TestQueryAndNotifyV1(t *testing.T) {
	s := newStub(t)
	c := newChannel(t, s, 1)
	s.reply = func(path string, p epay.Params) map[string]any {
		if path != "/api.php" || p["act"] != "order" || p["key"] != s.key {
			t.Errorf("V1 查单请求：%s %v", path, p)
		}
		return map[string]any{"code": 1, "msg": "succ", "trade_no": "U1", "out_trade_no": testOrder.TradeNo, "status": 0, "money": "10.50"}
	}
	if paid, err := c.Query(context.Background(), testOrder, "U1"); err != nil || paid != nil {
		t.Fatalf("未付款：%v %v", paid, err)
	}
	if s.got[0]["trade_no"] != "U1" {
		t.Errorf("有上游单号时按它查：%v", s.got[0])
	}
	s.reply = func(string, epay.Params) map[string]any {
		return map[string]any{"code": 1, "trade_no": "U1", "out_trade_no": testOrder.TradeNo, "status": 1, "money": "10.50", "api_trade_no": "W123", "buyer": "o_abc", "endtime": "2026-10-17 10:00:00", "type": "wxpay"}
	}
	paid, err := c.Query(context.Background(), testOrder, "")
	if err != nil || paid.Amount != 1050 || paid.TradeNo != "U1" || paid.APITradeNo != "W123" || paid.Buyer != "o_abc" ||
		!paid.PaidAt.Equal(time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("已付款：%+v %v", paid, err)
	}
	if s.got[1]["out_trade_no"] != testOrder.TradeNo {
		t.Errorf("没有上游单号时按平台订单号查：%v", s.got[1])
	}
	s.reply = func(string, epay.Params) map[string]any {
		return map[string]any{"code": 1, "trade_no": "U1", "out_trade_no": "other", "status": 1, "money": "10.50"}
	}
	if _, err := c.Query(context.Background(), testOrder, "U1"); err == nil {
		t.Error("订单号不符要报错")
	}

	n := epay.Params{"pid": "1001", "trade_no": "U1", "out_trade_no": testOrder.TradeNo, "type": "wxpay", "name": "商品", "money": "10.5", "trade_status": "TRADE_SUCCESS"}
	n["sign"], n["sign_type"] = epay.MD5Sign(n, s.key), "MD5"
	paid, err = c.VerifyNotify(n)
	if err != nil || paid.Amount != 1050 || paid.OutTradeNo != testOrder.TradeNo || !paid.PaidAt.IsZero() || strings.Contains(paid.Raw, n["sign"]) {
		t.Fatalf("回调：%+v %v", paid, err)
	}
	n["money"] = "1"
	if _, err := c.VerifyNotify(n); err == nil {
		t.Error("改了金额验签应失败")
	}
}

func TestV2(t *testing.T) {
	s := newStub(t)
	c := newChannel(t, s, 2)
	s.reply = func(path string, p epay.Params) map[string]any {
		if !epay.RSAVerify(epay.SignContent(p), p["sign"], s.merchant) || p["sign_type"] != "RSA" || p["timestamp"] == "" {
			t.Errorf("V2 请求签名：%v", p)
		}
		switch path {
		case "/api/pay/create":
			if p["method"] != "web" {
				t.Errorf("V2 下单：%v", p)
			}
			return s.sign(map[string]any{"code": 0, "trade_no": "U2", "pay_type": "qrcode", "pay_info": "weixin://wxpay/y"})
		case "/api/pay/query":
			return s.sign(map[string]any{"code": 0, "trade_no": "U2", "out_trade_no": testOrder.TradeNo, "status": 1, "money": "10.50", "refundmoney": 0.0, "flag": true})
		case "/api/merchant/info":
			return s.sign(map[string]any{"code": 0, "pid": 1001, "status": 1})
		}
		return map[string]any{"code": -5, "msg": "URL Error!"}
	}
	pay, err := c.Prepay(context.Background(), testOrder, "https://x")
	if err != nil || pay.TradeNo != "U2" || pay.InfoType != store.InfoQRCode || pay.Info != "weixin://wxpay/y" {
		t.Fatalf("V2 下单：%+v %v", pay, err)
	}
	paid, err := c.Query(context.Background(), testOrder, "U2")
	if err != nil || paid == nil || paid.Amount != 1050 {
		t.Fatalf("V2 查单（含浮点和布尔字段）：%+v %v", paid, err)
	}
	if err := c.Test(context.Background()); err != nil {
		t.Errorf("V2 测试连接：%v", err)
	}
	// 响应签名不对、时间戳过期都拒绝。
	s.reply = func(string, epay.Params) map[string]any {
		m := s.sign(map[string]any{"code": 0, "trade_no": "U2", "pay_type": "qrcode", "pay_info": "x"})
		m["pay_info"] = "y"
		return m
	}
	if _, err := c.Prepay(context.Background(), testOrder, "https://x"); err == nil || !strings.Contains(err.Error(), "验签失败") {
		t.Errorf("篡改的响应：%v", err)
	}
	s.reply = func(string, epay.Params) map[string]any {
		return s.sign(map[string]any{"code": 0, "trade_no": "U2", "pay_type": "qrcode", "pay_info": "x", "timestamp": strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)})
	}
	if _, err := c.Prepay(context.Background(), testOrder, "https://x"); err == nil || !strings.Contains(err.Error(), "300 秒") {
		t.Errorf("过期的时间戳：%v", err)
	}
	s.reply = func(string, epay.Params) map[string]any {
		return s.sign(map[string]any{"code": 0, "trade_no": "U2", "pay_type": "jsapi", "pay_info": "{}"})
	}
	if _, err := c.Prepay(context.Background(), testOrder, "https://x"); err == nil || !strings.Contains(err.Error(), "收银台用不了") {
		t.Errorf("不支持的付款方式：%v", err)
	}

	// 跳转付款地址：每次现签，带时间戳。
	u, err := c.SubmitURL(context.Background(), testOrder, "https://till.example.com")
	if err != nil || !strings.HasPrefix(u, s.srv.URL+"/api/pay/submit?") {
		t.Fatalf("跳转地址：%s %v", u, err)
	}
	q, _ := url.Parse(u)
	p := epay.Params{}
	for k, v := range q.Query() {
		p[k] = v[0]
	}
	if !epay.RSAVerify(epay.SignContent(p), p["sign"], s.merchant) || p["clientip"] != "" || p["timestamp"] == "" {
		t.Errorf("跳转地址参数：%v", p)
	}

	// V2 回调：RSA 验签加时间戳。
	n := epay.Params{"pid": "1001", "trade_no": "U2", "out_trade_no": testOrder.TradeNo, "type": "wxpay", "name": "商品", "money": "10.5",
		"trade_status": "TRADE_SUCCESS", "api_trade_no": "W9", "timestamp": strconv.FormatInt(time.Now().Unix(), 10)}
	n["sign"], _ = epay.RSASign(epay.SignContent(n), s.platform)
	n["sign_type"] = "RSA"
	if paid, err := c.VerifyNotify(n); err != nil || paid.APITradeNo != "W9" {
		t.Fatalf("V2 回调：%+v %v", paid, err)
	}
	n["timestamp"] = strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	n["sign"], _ = epay.RSASign(epay.SignContent(n), s.platform)
	if _, err := c.VerifyNotify(n); err == nil {
		t.Error("过期的回调时间戳应拒绝")
	}
}

func TestDecodedValues(t *testing.T) {
	m := roundTrip(map[string]any{"a": 10.0, "b": 10.5, "c": true, "d": false, "e": nil, "f": []int{1}, "g": "x", "h": 7})
	p := epay.DecodedValues(m)
	want := map[string]string{"a": "10", "b": "10.5", "c": "1", "g": "x", "h": "7"}
	if len(p) != len(want) {
		t.Fatalf("%v", p)
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s：%q，想要 %q", k, p[k], v)
		}
	}
}
