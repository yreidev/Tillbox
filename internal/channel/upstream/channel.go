// Package upstream 是上游易支付渠道：把订单转给别家易支付平台收款，按回调和查单逐单确认到账。
// V1 用 MD5 签名，V2 用 RSA 签名，协议见 docs/research/epay-protocol.md。
package upstream

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/secret"
	"tillbox/internal/store"
)

// TimestampWindow 是 V2 响应和回调里时间戳允许的误差。
const TimestampWindow = 300 * time.Second

// maxBody 是上游响应正文的上限。
const maxBody = 1 << 20

// NotifyPath 是渠道回调地址的路径。
func NotifyPath(id int64) string { return fmt.Sprintf("/web-api/channels/%d/notify", id) }

// Error 是上游返回的业务错误（code 不是成功码）。
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("上游返回错误（code=%s）", e.Code)
	}
	return fmt.Sprintf("上游返回错误（code=%s）：%s", e.Code, e.Msg)
}

// Channel 实现 channel.Confirmer。后台改了配置后由渠道注册表原地更新。
type Channel struct {
	channel.Base
	box  *secret.Box
	http *http.Client
	// Now 可以在测试里替换。
	Now func() time.Time

	mu     sync.RWMutex
	cfg    Config
	cfgErr error
}

func New(box *secret.Box) *Channel {
	return &Channel{
		box: box,
		http: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				Proxy:           http.ProxyFromEnvironment,
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Now: time.Now,
	}
}

// SetHTTPClient 替换 HTTP 客户端（测试用）。重定向一律不跟随。
func (c *Channel) SetHTTPClient(h *http.Client) {
	cp := *h
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = &cp
}

func (c *Channel) Configure(row store.Channel) {
	cfg, err := ParseConfig(row.Config)
	c.mu.Lock()
	c.cfg, c.cfgErr = cfg, err
	c.mu.Unlock()
	c.SetRow(row)
}

// Config 返回当前配置（密钥仍是加密的）。
func (c *Channel) Config() (Config, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg, c.cfgErr
}

func (c *Channel) PayTypes() []string {
	cfg, _ := c.Config()
	return slices.Clone(cfg.Types)
}

func (c *Channel) Ready(context.Context) error {
	cfg, err := c.Config()
	if err != nil {
		return err
	}
	if err := c.CheckEnabled(); err != nil {
		return err
	}
	return complete(cfg)
}

// Guide 只给支付方式：付款信息来自预下单，由收银台另外读取。
func (c *Channel) Guide(_ context.Context, o *store.Order) (channel.Guide, error) {
	return channel.Guide{App: channel.TypeLabel(o.PayType)}, nil
}

// creds 是解密后的密钥。
type creds struct {
	Config
	key      string
	priv     *rsa.PrivateKey
	platform *rsa.PublicKey
}

func (c *Channel) creds() (*creds, error) {
	cfg, err := c.Config()
	if err != nil {
		return nil, err
	}
	if err := complete(Config{APIURL: cfg.APIURL, PID: cfg.PID, Version: cfg.Version, Key: cfg.Key, PrivateKey: cfg.PrivateKey, PlatformPublicKey: cfg.PlatformPublicKey, Types: []string{"-"}}); err != nil {
		return nil, fmt.Errorf("%w：%v", channel.ErrNotConfigured, err) //nolint:errorlint // 按“未配置”处理，不能被识别成表单校验错误
	}
	cr := &creds{Config: cfg}
	if cfg.Version == 1 {
		if cr.key, err = c.box.Open(cfg.Key); err != nil {
			return nil, fmt.Errorf("商户密钥解密失败：%w", err)
		}
		return cr, nil
	}
	privText, err := c.box.Open(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("商户私钥解密失败：%w", err)
	}
	if cr.priv, err = epay.ParsePrivateKey(privText); err != nil {
		return nil, fmt.Errorf("商户私钥无效：%w", err)
	}
	if cr.platform, err = epay.ParsePublicKey(cfg.PlatformPublicKey); err != nil {
		return nil, fmt.Errorf("平台公钥无效：%w", err)
	}
	return cr, nil
}

// sign 给请求参数签名：V1 用 MD5，V2 带上时间戳用 RSA。
func (c *Channel) sign(cr *creds, p epay.Params) error {
	p["pid"] = cr.PID
	if cr.Version == 1 {
		p["sign"] = epay.MD5Sign(p, cr.key)
		p["sign_type"] = "MD5"
		return nil
	}
	p["timestamp"] = strconv.FormatInt(c.Now().Unix(), 10)
	s, err := epay.RSASign(epay.SignContent(p), cr.priv)
	if err != nil {
		return err
	}
	p["sign"], p["sign_type"] = s, "RSA"
	return nil
}

// verifyTimestamp 检查 V2 时间戳在误差范围内。
func (c *Channel) verifyTimestamp(ts string) error {
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("上游没有给时间戳")
	}
	if d := c.Now().Sub(time.Unix(n, 0)); d > TimestampWindow || d < -TimestampWindow {
		return errors.New("上游的时间戳和本机相差超过 300 秒")
	}
	return nil
}

func values(p epay.Params) url.Values {
	v := url.Values{}
	for k, s := range p {
		v.Set(k, s)
	}
	return v
}

// call 发请求并把响应解成 JSON（数字保留原文）。form 为 nil 时发 GET。
func (c *Channel) call(ctx context.Context, cr *creds, path string, query, form url.Values) (map[string]any, error) {
	u := cr.APIURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	method, body := http.MethodGet, io.Reader(nil)
	if form != nil {
		method, body = http.MethodPost, strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // 不把带密钥的地址写进错误
		}
		return nil, fmt.Errorf("请求上游失败：%w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("读取上游响应失败：%w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	if len(raw) > maxBody {
		return nil, errors.New("上游响应太大")
	}
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, fmt.Errorf("上游返回的不是 JSON：%s", snippet(raw))
	}
	return m, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return s
}

// str 取 JSON 字段的字符串形式：字符串原样，数字取原文，其余为空。
func str(m map[string]any, k string) string {
	switch x := m[k].(type) {
	case string:
		return x
	case json.Number:
		return string(x)
	}
	return ""
}

// check 检查成功码；V2 还要验平台签名和时间戳。
func (c *Channel) check(cr *creds, m map[string]any) error {
	code := str(m, "code")
	want := "1"
	if cr.Version == 2 {
		want = "0"
	}
	if code != want {
		msg := str(m, "msg")
		if code == "" {
			return fmt.Errorf("上游响应里没有 code：%s", msg)
		}
		return &Error{Code: code, Msg: msg}
	}
	if cr.Version == 1 {
		return nil
	}
	sign := str(m, "sign")
	if sign == "" {
		return errors.New("上游响应没有签名")
	}
	if err := c.verifyTimestamp(str(m, "timestamp")); err != nil {
		return err
	}
	if !epay.RSAVerify(epay.SignContent(epay.DecodedValues(m)), sign, cr.platform) {
		return errors.New("上游响应验签失败，请核对平台公钥")
	}
	return nil
}

// orderParams 是下单参数（预下单和跳转付款共用）。
func (c *Channel) orderParams(o *store.Order, base string) epay.Params {
	device := "pc"
	switch o.Device {
	case "mobile", "qq", "wechat", "alipay":
		device = "mobile"
	}
	ip := o.ClientIP
	if ip == "" {
		ip = "127.0.0.1"
	}
	return epay.Params{
		"type":         o.PayType,
		"out_trade_no": o.TradeNo,
		"notify_url":   base + NotifyPath(c.Row().ID),
		"return_url":   base + "/cashier/" + o.TradeNo,
		"name":         o.Name,
		"money":        epay.FormatCents(o.Amount),
		"clientip":     ip,
		"device":       device,
	}
}

func (c *Channel) Prepay(ctx context.Context, o *store.Order, base string) (channel.Payment, error) {
	var pay channel.Payment
	cr, err := c.creds()
	if err != nil {
		return pay, err
	}
	p := c.orderParams(o, base)
	path := "/mapi.php"
	if cr.Version == 2 {
		path = "/api/pay/create"
		p["method"] = "web"
	}
	if err := c.sign(cr, p); err != nil {
		return pay, err
	}
	m, err := c.call(ctx, cr, path, nil, values(p))
	if err != nil {
		return pay, err
	}
	if err := c.check(cr, m); err != nil {
		var ue *Error
		if errors.As(err, &ue) {
			if ae := amountLimit(o.PayType, o.Amount, ue); ae != nil {
				return pay, ae
			}
		}
		return pay, err
	}
	pay.TradeNo = str(m, "trade_no")
	if pay.TradeNo == "" {
		return pay, errors.New("上游没有返回订单号")
	}
	if cr.Version == 1 {
		for _, f := range []struct{ field, typ string }{
			{"qrcode", store.InfoQRCode}, {"payurl", store.InfoJump}, {"urlscheme", store.InfoURLScheme}, {"html", store.InfoSubmit},
		} {
			if v := str(m, f.field); v != "" {
				pay.InfoType, pay.Info = f.typ, v
				break
			}
		}
	} else {
		info := str(m, "pay_info")
		switch t := str(m, "pay_type"); t {
		case "qrcode":
			pay.InfoType, pay.Info = store.InfoQRCode, info
		case "jump":
			pay.InfoType, pay.Info = store.InfoJump, info
		case "urlscheme":
			pay.InfoType, pay.Info = store.InfoURLScheme, info
		case "html":
			pay.InfoType = store.InfoSubmit
		default:
			return pay, fmt.Errorf("上游给的付款方式 %q 收银台用不了", t)
		}
	}
	switch {
	case pay.InfoType == "":
		return pay, errors.New("上游没有返回付款信息")
	case pay.InfoType == store.InfoSubmit:
		pay.Info = "" // HTML 表单不入库，付款人点“去付款”时现签跳转地址
	case pay.Info == "" || len(pay.Info) > 2048:
		return pay, errors.New("上游给的付款信息是空的或太长")
	case pay.InfoType == store.InfoJump && !webURL(pay.Info):
		return pay, errors.New("上游给的付款链接不是 http(s) 地址")
	case pay.InfoType == store.InfoURLScheme && !appURL(pay.Info):
		return pay, errors.New("上游给的唤起链接不能用")
	}
	return pay, nil
}

func webURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// appURL 判断唤起 App 的链接能不能放进收银台的按钮：网址或 App 自定义协议，排除能在本页执行代码或读本地内容的协议。
func appURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "javascript", "vbscript", "data", "file", "blob", "about", "filesystem":
		return false
	}
	return true
}

func (c *Channel) SubmitURL(_ context.Context, o *store.Order, base string) (string, error) {
	cr, err := c.creds()
	if err != nil {
		return "", err
	}
	p := c.orderParams(o, base)
	delete(p, "clientip")
	delete(p, "device")
	path := "/submit.php"
	if cr.Version == 2 {
		path = "/api/pay/submit"
	}
	if err := c.sign(cr, p); err != nil {
		return "", err
	}
	return cr.APIURL + path + "?" + values(p).Encode(), nil
}

// paidFrom 把查单结果或回调参数转成确认的付款。
func paidFrom(get func(string) string, raw any) (*channel.Paid, error) {
	amount, ok := epay.ParseMoney(get("money"))
	if !ok {
		return nil, fmt.Errorf("上游给的金额 %q 不对", get("money"))
	}
	p := &channel.Paid{
		TradeNo: get("trade_no"), OutTradeNo: get("out_trade_no"), Type: get("type"), Amount: amount,
		APITradeNo: get("api_trade_no"), Buyer: get("buyer"),
	}
	if p.TradeNo == "" || p.OutTradeNo == "" {
		return nil, errors.New("上游没有给订单号")
	}
	if len(p.TradeNo) > 64 || len(p.APITradeNo) > 128 || len(p.Buyer) > 128 {
		return nil, errors.New("上游给的订单号或付款人太长")
	}
	if t, err := time.ParseInLocation(time.DateTime, get("endtime"), epay.CST); err == nil {
		p.PaidAt = t
	}
	b, _ := json.Marshal(raw)
	p.Raw = string(b)
	return p, nil
}

func (c *Channel) Query(ctx context.Context, o *store.Order, tradeNo string) (*channel.Paid, error) {
	cr, err := c.creds()
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if cr.Version == 1 {
		q := url.Values{"act": {"order"}, "pid": {cr.PID}, "key": {cr.key}}
		if tradeNo != "" {
			q.Set("trade_no", tradeNo)
		} else {
			q.Set("out_trade_no", o.TradeNo)
		}
		m, err = c.call(ctx, cr, "/api.php", q, nil)
	} else {
		p := epay.Params{}
		if tradeNo != "" {
			p["trade_no"] = tradeNo
		} else {
			p["out_trade_no"] = o.TradeNo
		}
		if err := c.sign(cr, p); err != nil {
			return nil, err
		}
		m, err = c.call(ctx, cr, "/api/pay/query", nil, values(p))
	}
	if err != nil {
		return nil, err
	}
	if err := c.check(cr, m); err != nil {
		return nil, err
	}
	if out := str(m, "out_trade_no"); out != "" && out != o.TradeNo {
		return nil, fmt.Errorf("上游查单返回的订单号 %s 和本单不符", out)
	}
	if str(m, "status") != "1" {
		return nil, nil
	}
	delete(m, "sign")
	paid, err := paidFrom(func(k string) string { return str(m, k) }, m)
	if err != nil {
		return nil, err
	}
	if paid.OutTradeNo != o.TradeNo {
		return nil, fmt.Errorf("上游查单返回的订单号 %s 和本单不符", paid.OutTradeNo)
	}
	return paid, nil
}

func (c *Channel) VerifyNotify(p map[string]string) (*channel.Paid, error) {
	cr, err := c.creds()
	if err != nil {
		return nil, err
	}
	ps := epay.Params(p)
	if cr.Version == 1 {
		if !epay.MD5Verify(ps, cr.key) {
			return nil, errors.New("回调验签失败")
		}
	} else {
		if p["sign_type"] != "RSA" || !epay.RSAVerify(epay.SignContent(ps), p["sign"], cr.platform) {
			return nil, errors.New("回调验签失败")
		}
		if err := c.verifyTimestamp(p["timestamp"]); err != nil {
			return nil, err
		}
	}
	if p["pid"] != cr.PID {
		return nil, fmt.Errorf("回调的商户号 %q 和渠道配置不符", p["pid"])
	}
	if p["trade_status"] != "TRADE_SUCCESS" {
		return nil, fmt.Errorf("回调的交易状态是 %q，不是成功", p["trade_status"])
	}
	raw := maps.Clone(p)
	delete(raw, "sign")
	return paidFrom(func(k string) string { return p[k] }, raw)
}

// Test 用商户信息接口验证地址、商户号和密钥：V1 是 act=query，V2 是 /api/merchant/info。
func (c *Channel) Test(ctx context.Context) error {
	cr, err := c.creds()
	if err != nil {
		return err
	}
	var m map[string]any
	if cr.Version == 1 {
		m, err = c.call(ctx, cr, "/api.php", url.Values{"act": {"query"}, "pid": {cr.PID}, "key": {cr.key}}, nil)
	} else {
		p := epay.Params{}
		if err := c.sign(cr, p); err != nil {
			return err
		}
		m, err = c.call(ctx, cr, "/api/merchant/info", nil, values(p))
	}
	if err != nil {
		return err
	}
	return c.check(cr, m)
}
