package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/channel/registry"
	"tillbox/internal/confirm"
	"tillbox/internal/epay"
	"tillbox/internal/merchant"
	"tillbox/internal/order"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// Server 处理易支付协议的请求。
type Server struct {
	Merchants *merchant.Service
	Orders    *order.Service
	Settings  *settings.Service
	Channels  *registry.Registry
	Confirm   *confirm.Service
	// BaseURL 返回本系统对外的地址（不带末尾的 /），拼收银台链接用。
	BaseURL func(r *http.Request) string
	// ClientIP 返回请求方的真实 IP。
	ClientIP func(r *http.Request) string
	// Now 可以在测试里替换。
	Now func() time.Time
}

// Register 挂上所有易支付路由。
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/submit.php", func(w http.ResponseWriter, r *http.Request) { s.submit(w, r, order.V1) })
	mux.HandleFunc("/mapi.php", s.mapi)
	mux.HandleFunc("/api.php", s.apiPHP)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		s.v2(w, r, strings.TrimPrefix(r.URL.Path, "/api/"), false)
	})
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Cache-Control", "no-store")
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(b)
}

func jsonMsg(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, map[string]any{"code": code, "msg": msg})
}

// sysmsg 是跳转下单出错时的 HTML 提示页（官方返回的也是 HTML，不是 JSON）。
func sysmsg(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>站点提示信息</title>
<style>body{margin:0;font:15px/1.6 system-ui,-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;background:#f4f5f7;color:#1c1f23}
.box{max-width:420px;margin:12vh auto 0;padding:28px 24px;background:#fff;border-radius:12px;box-shadow:0 2px 12px rgba(0,0,0,.06)}
h1{font-size:18px;margin:0 0 12px}p{margin:0;color:#41464c;word-break:break-all}</style></head>
<body><div class="box"><h1>站点提示信息</h1><p>%s</p></div></body></html>`, html.EscapeString(msg))
}

// apiError 是带易支付错误码的错误。
type apiError struct {
	code int
	msg  string
}

func (e *apiError) Error() string { return e.msg }

func fail(code int, format string, a ...any) *apiError {
	return &apiError{code: code, msg: fmt.Sprintf(format, a...)}
}

// verifyMerchant 对应官方 api_verify：密钥类型、V2 时间戳、签名。出错时错误码都是 -3。
func (s *Server) verifyMerchant(m *merchant.Merchant, p epay.Params, requireTimestamp bool) *apiError {
	signType := p["sign_type"]
	if m.KeyType == store.KeyTypeRSAOnly && signType != "RSA" {
		return fail(-3, "该商户只能使用RSA签名类型")
	}
	if requireTimestamp {
		ts := p["timestamp"]
		if phpEmpty(ts) {
			return fail(-3, "时间戳(timestamp)字段不能为空")
		}
		if d := s.now().Unix() - intval(ts); d > 300 || d < -300 {
			return fail(-3, "时间戳字段不正确，请检查服务器时间")
		}
	}
	if _, ok := p["sign"]; !ok {
		return fail(-3, "缺少签名参数")
	}
	if signType == "RSA" {
		if m.PublicKey == nil {
			return fail(-3, "签名校验失败，商户公钥错误")
		}
		if !epay.RSAVerify(epay.SignContent(p), p["sign"], m.PublicKey) {
			return fail(-3, "RSA签名校验失败")
		}
		return nil
	}
	if !epay.MD5Verify(p, m.Key) {
		name := signType
		if name == "" {
			name = "MD5"
		}
		return fail(-3, "%s签名校验失败", name)
	}
	return nil
}

// loadMerchant 对应官方：pid 转整数为 0 → "商户ID不能为空"，查不到 → "商户不存在！"，错误码 -1。
func (s *Server) loadMerchant(ctx context.Context, p epay.Params) (*merchant.Merchant, *apiError) {
	pid := intval(p["pid"])
	if pid == 0 {
		return nil, fail(-1, "商户ID不能为空")
	}
	m, err := s.Merchants.Get(ctx, pid)
	if errors.Is(err, merchant.ErrNotFound) {
		return nil, fail(-1, "商户不存在！")
	}
	if err != nil {
		slog.Error("读取商户失败", "pid", pid, "err", err)
		return nil, fail(-1, "系统繁忙，请稍后再试")
	}
	return m, nil
}

func (s *Server) cashierURL(r *http.Request, tradeNo string) string {
	return s.BaseURL(r) + "/cashier/" + tradeNo
}

var rejectedMethods = map[string]bool{"jsapi": true, "scan": true, "app": true, "applet": true}

// createOrder 是跳转下单和接口下单共用的建单流程（官方两者也是同一段代码）。
func (s *Server) createOrder(r *http.Request, p epay.Params, version int, isSubmit bool) (*store.Order, *apiError) {
	ctx := r.Context()
	m, e := s.loadMerchant(ctx, p)
	if e != nil {
		return nil, e
	}
	if e := s.verifyMerchant(m, p, version == order.V2); e != nil {
		return nil, e
	}
	if !m.Enabled {
		return nil, fail(-1, "商户已被封禁，无法支付！")
	}

	typ := p["type"]
	outTradeNo := p["out_trade_no"]
	notifyURL := p["notify_url"]
	returnURL := p["return_url"]
	name := p["name"]
	money := p["money"]
	clientIP := s.ClientIP(r)
	device := p["device"]
	method := p["method"]
	if !isSubmit {
		clientIP = p["clientip"]
		if device == "" {
			device = "pc"
		}
		if device == "jump" {
			method = "jump"
		}
	}

	switch {
	case phpEmpty(outTradeNo):
		return nil, fail(-1, "订单号(out_trade_no)不能为空")
	case phpEmpty(notifyURL):
		return nil, fail(-1, "通知地址(notify_url)不能为空")
	case isSubmit && phpEmpty(returnURL):
		return nil, fail(-1, "回调地址(return_url)不能为空")
	case phpEmpty(name):
		return nil, fail(-1, "商品名称(name)不能为空")
	case phpEmpty(money):
		return nil, fail(-1, "金额(money)不能为空")
	case !isSubmit && phpEmpty(typ) && method != "scan":
		return nil, fail(-1, "支付方式(type)不能为空")
	case !isSubmit && phpEmpty(clientIP):
		return nil, fail(-1, "用户IP地址(clientip)不能为空")
	case !outTradeNoRe.MatchString(outTradeNo):
		return nil, fail(-1, "订单号(out_trade_no)格式不正确")
	}
	amount, ok := epay.ParseMoney(money)
	if !ok {
		return nil, fail(-1, "金额不合法")
	}
	if !isSubmit {
		if _, err := netip.ParseAddr(clientIP); err != nil {
			return nil, fail(-1, "用户IP地址不合法")
		}
	}
	if !isURL(notifyURL) {
		return nil, fail(-1, "通知地址(notify_url)格式不正确")
	}
	if !isSubmit && rejectedMethods[method] {
		return nil, fail(-1, "暂不支持%s支付方式，请使用页面跳转或二维码支付", method)
	}
	if len(outTradeNo) > 64 {
		return nil, fail(-1, "订单号(out_trade_no)不能超过64个字符")
	}
	if len(notifyURL) > 1024 || len(returnURL) > 1024 || len(p["param"]) > 2048 {
		return nil, fail(-1, "通知地址、回调地址或附加参数太长")
	}
	if !m.AllowsURL(notifyURL) || (returnURL != "" && !m.AllowsURL(returnURL)) {
		return nil, fail(-1, "该域名不可发起支付，原因：域名没过白，请前往支付平台授权支付域名")
	}

	ch, _, found := s.Channels.Get(m.ChannelID)
	if !found {
		slog.Warn("商户没有可用的收款渠道", "pid", m.ID, "channel_id", m.ChannelID)
		return nil, fail(-2, "支付通道暂时不可用，请联系商户")
	}
	if err := ch.Ready(ctx); err != nil {
		slog.Warn("收款渠道没有就绪", "pid", m.ID, "channel", ch.Key(), "err", err)
		return nil, fail(-2, "支付通道暂时不可用，请联系商户")
	}
	_, confirming := ch.(channel.Confirmer)
	types := ch.PayTypes()
	if typ == "" && (len(types) == 1 || !confirming) {
		typ = types[0] // 上游渠道开放了多种方式时，留给付款人在收银台选
	}
	if typ != "" && !slices.Contains(types, typ) {
		label := channel.TypeNames[typ]
		if label == "" {
			label = "该支付方式（" + typ + "）"
		}
		return nil, fail(-1, "暂不支持%s，请使用%s付款", label, channel.TypeLabels(types))
	}

	name = cutUTF8(name, 127)
	o, _, err := s.Orders.Create(ctx, order.CreateInput{
		MerchantID: m.ID, OutTradeNo: outTradeNo, Version: version,
		Channel: ch.Key(), NoTail: confirming, PayType: typ, Name: name, Money: money, Amount: amount,
		NotifyURL: notifyURL, ReturnURL: returnURL, Param: p["param"], ClientIP: clientIP, Device: device,
	})
	if err != nil {
		var full *order.PoolFullError
		if errors.As(err, &full) || strings.HasPrefix(err.Error(), "该订单(") {
			return nil, fail(-1, "%s", err.Error())
		}
		slog.Error("建单失败", "pid", m.ID, "out_trade_no", outTradeNo, "err", err)
		return nil, fail(-1, "创建订单失败，请返回重试！")
	}
	// 上游渠道：知道支付方式就当场向上游预下单。失败时订单保持待支付，调用方用同一订单号重试会再预下单。
	if cf, _ := s.Confirm.Confirmer(o.Channel); cf != nil && typ != "" {
		if _, err := s.Confirm.Prepare(ctx, o, typ, s.BaseURL(r)); err != nil {
			return nil, fail(-2, "支付通道暂时不可用，请稍后重试")
		}
	}
	return o, nil
}

// submit 是跳转下单：查询串里有 pid 就整组用查询串，否则整组用表单。成功后 303 到收银台。
func (s *Server) submit(w http.ResponseWriter, r *http.Request, version int) {
	var p epay.Params
	if q := r.URL.Query(); q.Has("pid") {
		p = flatten(q)
	} else if f := postForm(r); f.Has("pid") {
		p = flatten(f)
	} else {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		_, _ = w.Write([]byte("你还未配置支付接口商户！"))
		return
	}
	o, e := s.createOrder(r, p, version, true)
	if e != nil {
		sysmsg(w, e.msg)
		return
	}
	http.Redirect(w, r, s.cashierURL(r, o.TradeNo), http.StatusSeeOther)
}

// mapi 是 V1 接口下单，只读 POST 表单。
func (s *Server) mapi(w http.ResponseWriter, r *http.Request) {
	f := postForm(r)
	if !f.Has("pid") {
		jsonMsg(w, -4, "未传入任何参数")
		return
	}
	o, e := s.createOrder(r, flatten(f), order.V1, false)
	if e != nil {
		jsonMsg(w, e.code, e.msg)
		return
	}
	u := s.cashierURL(r, o.TradeNo)
	writeJSON(w, map[string]any{"code": 1, "trade_no": o.TradeNo, "payurl": u, "qrcode": u})
}

var apiPathRe = regexp.MustCompile(`^(.[a-zA-Z0-9_]+)/(.[a-zA-Z0-9_]+)$`)

// apiPHP 是 /api.php：带 s 参数时等同 /api/{s}，否则按 act 分发 V1 接口。
func (s *Server) apiPHP(w http.ResponseWriter, r *http.Request) {
	if q := r.URL.Query(); q.Has("s") {
		s.v2(w, r, q.Get("s"), true)
		return
	}
	p := merged(r)
	switch p["act"] {
	case "query":
		s.v1Query(w, r, p)
	case "settle":
		s.v1Settle(w, r, p)
	case "order":
		s.v1Order(w, r, p)
	case "orders":
		s.v1Orders(w, r, p)
	case "refund":
		s.v1Refund(w, r, p)
	default:
		jsonMsg(w, -5, "No Act!")
	}
}

// v2 是 /api/{类}/{方法}。viaPHP 表示从 /api.php?s= 进来，这时查询串里的 s 只是路由，不算参数。
func (s *Server) v2(w http.ResponseWriter, r *http.Request, path string, viaPHP bool) {
	if !apiPathRe.MatchString(path) {
		jsonMsg(w, -5, "URL Error!")
		return
	}
	path = strings.ToLower(path)
	switch path {
	case "pay/submit":
		s.submit(w, r, order.V2)
		return
	case "pay/create":
		f := postForm(r)
		if !f.Has("pid") {
			jsonMsg(w, -4, "未传入任何参数")
			return
		}
		o, e := s.createOrder(r, flatten(f), order.V2, false)
		if e != nil {
			jsonMsg(w, e.code, e.msg)
			return
		}
		payType := "qrcode"
		if p := flatten(f); p["method"] == "jump" || p["device"] == "jump" || (p["device"] != "" && p["device"] != "pc") {
			payType = "jump"
		}
		s.signed(w, map[string]any{"code": 0, "trade_no": o.TradeNo, "pay_type": payType, "pay_info": s.cashierURL(r, o.TradeNo)})
		return
	}
	handlers := map[string]func(*http.Request, *merchant.Merchant, epay.Params) (map[string]any, *apiError){
		"pay/query":       s.v2Query,
		"pay/refund":      s.v2Refund,
		"pay/refundquery": s.v2RefundQuery,
		"pay/close":       s.v2Close,
		"merchant/info":   s.v2MerchantInfo,
		"merchant/orders": s.v2MerchantOrders,
	}
	h, ok := handlers[path]
	if !ok {
		jsonMsg(w, -5, "接口方法不存在")
		return
	}
	p := merged(r)
	if viaPHP && !postForm(r).Has("s") {
		delete(p, "s")
	}
	if _, ok := p["pid"]; !ok {
		jsonMsg(w, -4, "未传入任何参数")
		return
	}
	m, e := s.loadMerchant(r.Context(), p)
	if e != nil {
		jsonMsg(w, e.code, e.msg)
		return
	}
	if !m.Enabled {
		jsonMsg(w, -1, "商户已被封禁")
		return
	}
	if e := s.verifyMerchant(m, p, true); e != nil {
		jsonMsg(w, e.code, e.msg)
		return
	}
	res, e := h(r, m, p)
	if e != nil {
		jsonMsg(w, e.code, e.msg)
		return
	}
	s.signed(w, res)
}

// signed 给 V2 响应加上 timestamp、sign_type 和平台签名。签名字段只放字符串和整数。
func (s *Server) signed(w http.ResponseWriter, res map[string]any) {
	ctx := context.Background()
	_, privText, err := s.Settings.PlatformKey(ctx)
	if err != nil {
		slog.Error("读取平台私钥失败", "err", err)
		jsonMsg(w, -1, "平台密钥不可用")
		return
	}
	priv, err := epay.ParsePrivateKey(privText)
	if err != nil {
		slog.Error("平台私钥无效", "err", err)
		jsonMsg(w, -1, "平台密钥不可用")
		return
	}
	res["timestamp"] = strconv.FormatInt(s.now().Unix(), 10)
	res["sign_type"] = "RSA"
	sign, err := epay.RSASign(epay.SignContent(epay.SignValues(res)), priv)
	if err != nil {
		jsonMsg(w, -1, "签名失败")
		return
	}
	res["sign"] = sign
	writeJSON(w, res)
}

// findOrder 按 trade_no 或 out_trade_no 找商户的订单，trade_no 优先。都没给返回 nil。
func (s *Server) findOrder(ctx context.Context, m *merchant.Merchant, p epay.Params) (*store.Order, bool, error) {
	tradeNo, outTradeNo := p["trade_no"], p["out_trade_no"]
	if phpEmpty(tradeNo) && phpEmpty(outTradeNo) {
		return nil, false, nil
	}
	if phpEmpty(tradeNo) {
		tradeNo = ""
	}
	o, err := s.Orders.Find(ctx, m.ID, tradeNo, outTradeNo)
	return o, true, err
}

// orNull 空串输出 null（官方这些字段在库里是 NULL）。
func orNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func addTime(o *store.Order) string { return epay.FormatTime(store.Time(o.CreatedAt)) }

func endTime(o *store.Order) string {
	if o.Status != store.OrderPaid {
		return ""
	}
	return epay.FormatTime(store.Time(o.PaidAt))
}

// keyAuth 是 V1 api.php 的明文密钥鉴权。出错时错误码都是 -3。
func (s *Server) keyAuth(ctx context.Context, p epay.Params) (*merchant.Merchant, *apiError) {
	m, err := s.Merchants.Get(ctx, intval(p["pid"]))
	if errors.Is(err, merchant.ErrNotFound) {
		return nil, fail(-3, "商户ID不存在")
	}
	if err != nil {
		slog.Error("读取商户失败", "err", err)
		return nil, fail(-3, "商户ID不存在")
	}
	if m.Key == "" || subtle.ConstantTimeCompare([]byte(p["key"]), []byte(m.Key)) != 1 {
		return nil, fail(-3, "商户密钥错误")
	}
	if m.KeyType == store.KeyTypeRSAOnly {
		return nil, fail(-3, "该商户只能使用RSA签名类型")
	}
	return m, nil
}

func enabledInt(m *merchant.Merchant) int {
	if m.Enabled {
		return 1
	}
	return 0
}

func (s *Server) v1Query(w http.ResponseWriter, r *http.Request, p epay.Params) {
	m, e := s.keyAuth(r.Context(), p)
	if e != nil {
		jsonMsg(w, e.code, e.msg)
		return
	}
	st, err := s.Orders.MerchantStats(r.Context(), m.ID)
	if err != nil {
		jsonMsg(w, -1, "查询失败")
		return
	}
	writeJSON(w, map[string]any{
		"code": 1, "pid": m.ID, "key": m.Key, "active": enabledInt(m), "money": "0.00",
		"type": 0, "account": "", "username": "",
		"orders": st.Total, "orders_today": st.PaidToday, "orders_lastday": st.PaidYesterday,
	})
}

func (s *Server) v1Settle(w http.ResponseWriter, r *http.Request, p epay.Params) {
	if _, e := s.keyAuth(r.Context(), p); e != nil {
		jsonMsg(w, e.code, e.msg)
		return
	}
	writeJSON(w, map[string]any{"code": 1, "msg": "查询结算记录成功！", "data": []any{}})
}

func (s *Server) v1Order(w http.ResponseWriter, r *http.Request, p epay.Params) {
	m, e := s.keyAuth(r.Context(), p)
	if e != nil {
		jsonMsg(w, e.code, e.msg)
		return
	}
	o, given, err := s.findOrder(r.Context(), m, p)
	if !given {
		jsonMsg(w, -4, "订单号不能为空")
		return
	}
	if err != nil {
		jsonMsg(w, -1, "订单号不存在")
		return
	}
	writeJSON(w, map[string]any{
		"code": 1, "msg": "succ",
		"trade_no": o.TradeNo, "out_trade_no": o.OutTradeNo,
		"api_trade_no": orNull(o.APITradeNo), "bill_trade_no": nil,
		"type": o.PayType, "pid": o.MerchantID,
		"addtime": addTime(o), "endtime": orNull(endTime(o)),
		"name": o.Name, "money": epay.FormatCents(o.Amount),
		"param": orNull(o.Param), "buyer": orNull(o.Buyer),
		"status": order.ExternalStatus(o), "payurl": nil,
	})
}

func pageArgs(p epay.Params) (limit, offset int) {
	limit = 10
	if v, ok := p["limit"]; ok {
		limit = int(intval(v))
	}
	limit = min(max(limit, 0), 50)
	offset = max(int(intval(p["offset"])), 0)
	return
}

func statusArg(p epay.Params) *int {
	v, ok := p["status"]
	if !ok {
		return nil
	}
	n := int(intval(v))
	return &n
}

func (s *Server) v1Orders(w http.ResponseWriter, r *http.Request, p epay.Params) {
	m, e := s.keyAuth(r.Context(), p)
	if e != nil {
		jsonMsg(w, e.code, e.msg)
		return
	}
	limit, offset := pageArgs(p)
	os, err := s.Orders.ListForMerchant(r.Context(), m.ID, statusArg(p), limit, offset)
	if err != nil {
		jsonMsg(w, -1, "查询订单记录失败！")
		return
	}
	data := make([]map[string]any, 0, len(os))
	for i := range os {
		o := &os[i]
		data = append(data, map[string]any{
			"trade_no": o.TradeNo, "out_trade_no": o.OutTradeNo, "type": o.PayType, "pid": o.MerchantID,
			"addtime": addTime(o), "endtime": orNull(endTime(o)), "name": o.Name,
			"money": epay.FormatCents(o.Amount), "param": orNull(o.Param), "buyer": orNull(o.Buyer),
			"status": order.ExternalStatus(o),
		})
	}
	writeJSON(w, map[string]any{"code": 1, "msg": "查询订单记录成功！", "count": len(data), "data": data})
}

// refundResult 是退款的结论：一期没有 API 退款，已支付的订单返回"不支持"。
func refundResult(o *store.Order) *apiError {
	if o.Status != store.OrderPaid {
		return fail(-1, "该订单状态不支持退款！")
	}
	return fail(-1, "当前支付通道不支持API退款")
}

func (s *Server) v1Refund(w http.ResponseWriter, r *http.Request, p epay.Params) {
	m, e := s.keyAuth(r.Context(), p)
	if e != nil {
		jsonMsg(w, e.code, e.msg)
		return
	}
	if _, ok := epay.ParseMoney(p["money"]); !ok {
		jsonMsg(w, -1, "金额输入错误")
		return
	}
	o, given, err := s.findOrder(r.Context(), m, p)
	if !given {
		jsonMsg(w, -4, "订单号不能为空")
		return
	}
	if err != nil {
		jsonMsg(w, -1, "当前订单不存在！")
		return
	}
	e = refundResult(o)
	jsonMsg(w, e.code, e.msg)
}

// dropEmpty 去掉空值字段（官方 V2 查询类接口用 array_filter + isEmpty 去掉空值）。
func dropEmpty(m map[string]any) map[string]any {
	for k, v := range m {
		if s, ok := v.(string); ok && epay.IsEmpty(s) {
			delete(m, k)
		}
		if v == nil {
			delete(m, k)
		}
	}
	return m
}

func (s *Server) v2Query(r *http.Request, m *merchant.Merchant, p epay.Params) (map[string]any, *apiError) {
	o, given, err := s.findOrder(r.Context(), m, p)
	if !given {
		return nil, fail(-1, "订单号不能为空")
	}
	if err != nil {
		return nil, fail(-1, "订单号不存在")
	}
	return dropEmpty(map[string]any{
		"code": 0, "trade_no": o.TradeNo, "out_trade_no": o.OutTradeNo, "api_trade_no": o.APITradeNo,
		"type": o.PayType, "pid": o.MerchantID, "addtime": addTime(o), "endtime": endTime(o),
		"name": o.Name, "money": epay.FormatCents(o.Amount), "param": o.Param, "buyer": o.Buyer,
		"clientip": o.ClientIP, "status": order.ExternalStatus(o),
	}), nil
}

func (s *Server) v2Refund(r *http.Request, m *merchant.Merchant, p epay.Params) (map[string]any, *apiError) {
	if money := p["money"]; !phpEmpty(money) {
		if _, ok := epay.ParseMoney(money); !ok {
			return nil, fail(-1, "金额输入错误")
		}
	}
	o, given, err := s.findOrder(r.Context(), m, p)
	if !given {
		return nil, fail(-1, "订单号不能为空")
	}
	if err != nil {
		if phpEmpty(p["trade_no"]) {
			return nil, fail(-1, "当前订单不存在！")
		}
		// 官方按 trade_no 查不到时，由退款逻辑返回带签名的 code=-1。
		return map[string]any{"code": -1, "msg": "当前订单不存在！"}, nil
	}
	e := refundResult(o)
	return map[string]any{"code": e.code, "msg": e.msg}, nil
}

func (s *Server) v2RefundQuery(_ *http.Request, _ *merchant.Merchant, p epay.Params) (map[string]any, *apiError) {
	if phpEmpty(p["refund_no"]) && phpEmpty(p["out_refund_no"]) {
		return nil, fail(-1, "商户退款单号不能为空")
	}
	return nil, fail(-1, "退款记录不存在")
}

func (s *Server) v2Close(r *http.Request, m *merchant.Merchant, p epay.Params) (map[string]any, *apiError) {
	o, given, err := s.findOrder(r.Context(), m, p)
	if !given {
		return nil, fail(-1, "订单号不能为空")
	}
	if err != nil {
		if phpEmpty(p["trade_no"]) {
			return nil, fail(-1, "当前订单不存在！")
		}
		return map[string]any{"code": -1, "msg": "当前订单不存在！"}, nil
	}
	if _, err := s.Orders.Close(r.Context(), o.ID); err != nil {
		if errors.Is(err, order.ErrCannotClose) {
			return map[string]any{"code": -1, "msg": err.Error()}, nil
		}
		slog.Error("关单失败", "trade_no", o.TradeNo, "err", err)
		return nil, fail(-1, "关闭订单失败，请重试")
	}
	return map[string]any{"code": 0, "msg": "订单关闭成功"}, nil
}

func (s *Server) v2MerchantInfo(r *http.Request, m *merchant.Merchant, _ epay.Params) (map[string]any, *apiError) {
	st, err := s.Orders.MerchantStats(r.Context(), m.ID)
	if err != nil {
		return nil, fail(-1, "查询失败")
	}
	return dropEmpty(map[string]any{
		"code": 0, "pid": m.ID, "status": enabledInt(m), "pay_status": enabledInt(m), "settle_status": 0,
		"money": "0.00", "settle_type": 0, "settle_account": "", "settle_name": "",
		"order_num": st.Total, "order_num_today": st.PaidToday, "order_num_lastday": st.PaidYesterday,
		"order_money_today": phpFloat(st.AmountToday), "order_money_lastday": phpFloat(st.AmountYesterday),
	}), nil
}

func (s *Server) v2MerchantOrders(r *http.Request, m *merchant.Merchant, p epay.Params) (map[string]any, *apiError) {
	limit, offset := pageArgs(p)
	os, err := s.Orders.ListForMerchant(r.Context(), m.ID, statusArg(p), limit, offset)
	if err != nil {
		return nil, fail(-1, "查询订单记录失败！")
	}
	data := make([]map[string]any, 0, len(os))
	for i := range os {
		o := &os[i]
		data = append(data, map[string]any{
			"trade_no": o.TradeNo, "out_trade_no": o.OutTradeNo, "api_trade_no": orNull(o.APITradeNo),
			"type": o.PayType, "pid": o.MerchantID, "addtime": addTime(o), "endtime": orNull(endTime(o)),
			"name": o.Name, "money": epay.FormatCents(o.Amount), "param": orNull(o.Param),
			"buyer": orNull(o.Buyer), "clientip": o.ClientIP, "status": order.ExternalStatus(o), "refundmoney": nil,
		})
	}
	return map[string]any{"code": 0, "data": data}, nil
}
