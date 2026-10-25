package admin_test

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"tillbox/internal/admin"
	"tillbox/internal/apptest"
	"tillbox/internal/epay"
	"tillbox/internal/merchant"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

func decode[T any](t *testing.T, r *apptest.Response) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("响应不是预期的 JSON：%v\n%s", err, r.Body)
	}
	return v
}

// expect 检查状态码，错误时检查提示里含有 want。
func expect(t *testing.T, r *apptest.Response, status int, want string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("状态码 %d，应为 %d：%s", r.Status, status, r.Body)
	}
	if want != "" && !strings.Contains(string(r.Body), want) {
		t.Fatalf("响应里没有 %q：%s", want, r.Body)
	}
}

func raw(e *apptest.Env, method, path, body string, header map[string]string) *apptest.Response {
	req, _ := http.NewRequest(method, e.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return e.Do(req)
}

func audits(t *testing.T, e *apptest.Env) []string {
	t.Helper()
	var logs []store.AuditLog
	if err := e.DB().NewSelect().Model(&logs).Order("id ASC").Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(logs))
	for i, l := range logs {
		out[i] = l.Action
	}
	return out
}

func hasAudit(t *testing.T, e *apptest.Env, action string) bool {
	t.Helper()
	for _, a := range audits(t, e) {
		if a == action {
			return true
		}
	}
	return false
}

// order 用 mapi 下一单，返回平台订单号。
func order(t *testing.T, e *apptest.Env, m *apptest.Merchant, out, money, name string) string {
	t.Helper()
	res := e.PostForm("/mapi.php", m.SignMD5(map[string]string{
		"type": "alipay", "out_trade_no": out, "notify_url": e.Hook.URL + "/n", "return_url": "https://shop.example.com/r",
		"name": name, "money": money, "clientip": "1.1.1.1",
	})).JSON(t)
	tn, _ := res["trade_no"].(string)
	if tn == "" {
		t.Fatalf("下单失败：%v", res)
	}
	return tn
}

func TestAuth(t *testing.T) {
	e := apptest.New(t)
	csrf := map[string]string{"X-Tillbox": "1"}

	expect(t, e.Get("/web-api/auth/me"), http.StatusUnauthorized, "请先登录")
	expect(t, e.Get("/web-api/admin/orders"), http.StatusUnauthorized, "")
	expect(t, raw(e, "POST", "/web-api/auth/login", `{"password":"`+e.App.AdminPassword+`"}`, nil), http.StatusForbidden, "X-Tillbox")
	expect(t, raw(e, "POST", "/web-api/auth/login", `{"password":"wrong-password"}`, csrf), http.StatusUnauthorized, "密码不对")
	expect(t, raw(e, "POST", "/web-api/auth/login", `{"password":""}`, csrf), http.StatusUnauthorized, "密码不对")
	expect(t, raw(e, "POST", "/web-api/auth/login", `{bad`, csrf), http.StatusBadRequest, "请求格式不对")

	res := raw(e, "POST", "/web-api/auth/login", `{"password":"`+e.App.AdminPassword+`"}`, csrf)
	expect(t, res, http.StatusOK, "")
	var cookie *http.Cookie
	for _, c := range (&http.Response{Header: res.Header}).Cookies() {
		if c.Name == "tillbox_session" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure || cookie.MaxAge != 7*24*3600 {
		t.Fatalf("会话 cookie 属性不对：%+v", cookie)
	}
	withCookie := map[string]string{"Cookie": "tillbox_session=" + cookie.Value}
	me := decode[map[string]any](t, raw(e, "GET", "/web-api/auth/me", "", withCookie))
	if me["demo"] != true || me["site_name"] == "" || me["api_url"] != e.URL+"/" {
		t.Errorf("当前用户：%v", me)
	}
	// 带了 cookie 但没带 CSRF 头的修改请求一律拒绝。
	expect(t, raw(e, "POST", "/web-api/admin/merchants", `{"name":"x"}`, withCookie), http.StatusForbidden, "X-Tillbox")
	expect(t, raw(e, "GET", "/web-api/auth/me", "", map[string]string{"Cookie": "tillbox_session=forged"}), http.StatusUnauthorized, "")

	// 改密码：其他会话失效，当前会话保留。
	a := e.Login()
	expect(t, a.Call("POST", "/web-api/auth/password", map[string]string{"old_password": "wrong", "new_password": "new-password-1"}), http.StatusBadRequest, "原密码不对")
	expect(t, a.Call("POST", "/web-api/auth/password", map[string]string{"old_password": e.App.AdminPassword, "new_password": "short"}), http.StatusBadRequest, "至少 8 位")
	expect(t, a.Call("POST", "/web-api/auth/password", map[string]string{"old_password": e.App.AdminPassword, "new_password": "new-password-1"}), http.StatusOK, "")
	expect(t, a.Call("GET", "/web-api/auth/me", nil), http.StatusOK, "site_name")
	expect(t, raw(e, "GET", "/web-api/auth/me", "", withCookie), http.StatusUnauthorized, "")
	expect(t, raw(e, "POST", "/web-api/auth/login", `{"password":"`+e.App.AdminPassword+`"}`, csrf), http.StatusUnauthorized, "")
	expect(t, raw(e, "POST", "/web-api/auth/login", `{"password":"new-password-1"}`, csrf), http.StatusOK, "")

	// 退出后会话失效。
	expect(t, a.Call("POST", "/web-api/auth/logout", nil), http.StatusOK, "")
	expect(t, a.Call("GET", "/web-api/auth/me", nil), http.StatusUnauthorized, "")

	for _, want := range []string{"auth.login", "auth.password"} {
		if !hasAudit(t, e, want) {
			t.Errorf("没有审计记录 %s：%v", want, audits(t, e))
		}
	}
}

func TestLoginLockout(t *testing.T) {
	e := apptest.New(t)
	csrf := map[string]string{"X-Tillbox": "1"}
	for range 10 {
		expect(t, raw(e, "POST", "/web-api/auth/login", `{"password":"wrong-password"}`, csrf), http.StatusUnauthorized, "")
	}
	// 第 11 次密码对了也先拒绝。
	expect(t, raw(e, "POST", "/web-api/auth/login", `{"password":"`+e.App.AdminPassword+`"}`, csrf), http.StatusTooManyRequests, "15 分钟")
}

func TestResetPassword(t *testing.T) {
	e := apptest.New(t)
	csrf := map[string]string{"X-Tillbox": "1"}
	a := e.Login()
	pw, err := admin.ResetPassword(e.Ctx, e.DB())
	if err != nil || len(pw) < 12 || pw == e.App.AdminPassword {
		t.Fatalf("重置密码：%q %v", pw, err)
	}
	// 所有会话失效，旧密码不能用，新密码能用。
	expect(t, a.Call("GET", "/web-api/auth/me", nil), http.StatusUnauthorized, "")
	expect(t, raw(e, "POST", "/web-api/auth/login", `{"password":"`+e.App.AdminPassword+`"}`, csrf), http.StatusUnauthorized, "")
	expect(t, raw(e, "POST", "/web-api/auth/login", `{"password":"`+pw+`"}`, csrf), http.StatusOK, "")
	if n, _ := e.DB().NewSelect().Model((*store.Admin)(nil)).Count(e.Ctx); n != 1 {
		t.Errorf("管理员应只有一个：%d", n)
	}
}

func TestMerchants(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	_, pub, _ := epay.GenerateKeyPair()

	for _, c := range []struct {
		in   merchant.Input
		want string
	}{
		{merchant.Input{Name: " "}, "商户名称不能为空"},
		{merchant.Input{Name: strings.Repeat("长", 65)}, "不超过 64"},
		{merchant.Input{Name: "x", RSAPublicKey: "not-a-key"}, "RSA 公钥无效"},
		{merchant.Input{Name: "x", KeyType: store.KeyTypeRSAOnly}, "只收 RSA"},
		{merchant.Input{Name: "x", KeyType: 9}, "密钥类型"},
		{merchant.Input{Name: "x", Domains: "https://a.com/"}, "域名"},
		{merchant.Input{Name: "x", Remark: strings.Repeat("备", 501)}, "备注"},
	} {
		expect(t, a.Call("POST", "/web-api/admin/merchants", c.in), http.StatusBadRequest, c.want)
	}

	created := decode[struct {
		Merchant store.Merchant `json:"merchant"`
		Key      string         `json:"key"`
	}](t, a.Call("POST", "/web-api/admin/merchants", merchant.Input{
		Name: " 商城 ", Enabled: true, RSAPublicKey: pub, Domains: "Shop.Example.com\n*.example.com,shop.example.com 127.0.0.1",
	}))
	m := created.Merchant
	if m.ID != merchant.FirstID || m.Name != "商城" || len(created.Key) != 32 || m.Domains != "shop.example.com\n*.example.com\n127.0.0.1" {
		t.Fatalf("新建商户：%+v %q", m, created.Key)
	}
	if strings.Contains(string(a.Call("GET", "/web-api/admin/merchants", nil).Body), created.Key) {
		t.Error("商户列表不该带出密钥")
	}
	got := decode[map[string]string](t, a.Call("GET", fmt.Sprintf("/web-api/admin/merchants/%d/key", m.ID), nil))
	if got["key"] != created.Key {
		t.Errorf("查看密钥：%v", got)
	}
	tm := &apptest.Merchant{ID: m.ID, Key: created.Key}
	tn := order(t, e, tm, "M1", "1.00", "x")

	list := decode[struct {
		Items []struct {
			Merchant store.Merchant `json:"merchant"`
			Orders   int64          `json:"orders"`
		} `json:"items"`
	}](t, a.Call("GET", "/web-api/admin/merchants", nil))
	if len(list.Items) != 1 || list.Items[0].Orders != 1 {
		t.Errorf("商户列表：%+v", list)
	}

	// 重置密钥后旧密钥立即失效。
	reset := decode[map[string]any](t, a.Call("POST", fmt.Sprintf("/web-api/admin/merchants/%d/reset-key", m.ID), nil))
	if reset["key"] == "" || reset["key"] == created.Key {
		t.Fatalf("重置密钥：%v", reset)
	}
	res := e.PostForm("/mapi.php", tm.SignMD5(map[string]string{"type": "alipay", "out_trade_no": "M2", "notify_url": "https://shop.example.com/n", "name": "x", "money": "1"})).JSON(t)
	if res["code"] == json.Number("1") {
		t.Error("旧密钥还能下单")
	}
	tm.Key, _ = reset["key"].(string)
	order(t, e, tm, "M3", "1.00", "x")

	// 修改：停用后不能下单。
	upd := decode[map[string]store.Merchant](t, a.Call("PUT", fmt.Sprintf("/web-api/admin/merchants/%d", m.ID), merchant.Input{Name: "商城二", Enabled: false}))
	if upd["merchant"].Name != "商城二" || upd["merchant"].Enabled || upd["merchant"].RSAPublicKey != "" {
		t.Errorf("修改商户：%+v", upd)
	}
	res = e.PostForm("/mapi.php", tm.SignMD5(map[string]string{"type": "alipay", "out_trade_no": "M4", "notify_url": "https://shop.example.com/n", "name": "x", "money": "1"})).JSON(t)
	if res["code"] == json.Number("1") {
		t.Error("停用的商户还能下单")
	}
	expect(t, a.Call("PUT", "/web-api/admin/merchants/999", merchant.Input{Name: "x"}), http.StatusNotFound, "商户不存在")
	expect(t, a.Call("PUT", "/web-api/admin/merchants/abc", merchant.Input{Name: "x"}), http.StatusBadRequest, "编号不对")

	// 有订单的不能删；没订单的可以。
	expect(t, a.Call("DELETE", fmt.Sprintf("/web-api/admin/merchants/%d", m.ID), nil), http.StatusBadRequest, "只能停用")
	other := decode[struct {
		Merchant store.Merchant `json:"merchant"`
	}](t, a.Call("POST", "/web-api/admin/merchants", merchant.Input{Name: "临时"})).Merchant
	if other.ID != m.ID+1 {
		t.Errorf("商户号应递增：%d", other.ID)
	}
	expect(t, a.Call("DELETE", fmt.Sprintf("/web-api/admin/merchants/%d", other.ID), nil), http.StatusOK, "")
	expect(t, a.Call("DELETE", fmt.Sprintf("/web-api/admin/merchants/%d", other.ID), nil), http.StatusNotFound, "")
	expect(t, a.Call("GET", "/web-api/admin/merchants/999/key", nil), http.StatusNotFound, "")
	_ = tn

	for _, want := range []string{"merchant.create", "merchant.view_key", "merchant.reset_key", "merchant.update", "merchant.delete"} {
		if !hasAudit(t, e, want) {
			t.Errorf("没有审计记录 %s", want)
		}
	}
	logs := decode[struct {
		Items []store.AuditLog `json:"items"`
		Total int              `json:"total"`
	}](t, a.Call("GET", "/web-api/admin/audit?page_size=2", nil))
	if len(logs.Items) != 2 || logs.Total < 6 || logs.Items[0].ID < logs.Items[1].ID {
		t.Errorf("审计日志分页：%+v", logs)
	}
}

type orderList struct {
	Items []struct {
		store.Order
		State        string `json:"state"`
		MerchantName string `json:"merchant_name"`
	} `json:"items"`
	Total int `json:"total"`
}

func TestOrders(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	m := e.NewMerchant(nil)
	paid := order(t, e, m, "P1", "10.00", "已付")
	pending := order(t, e, m, "P2", "20", "待付")
	closed := order(t, e, m, "P3", "30", "=cmd|' /C calc'!A0")
	expired := order(t, e, m, "P4", "40", "过期")
	e.Pay(paid)
	expect(t, a.Call("POST", "/web-api/admin/orders/"+strconv.FormatInt(e.Order(closed).ID, 10)+"/close", nil), http.StatusOK, "")
	// 到期了但还没被后台任务标成过期：按到期时间算作过期。
	if _, err := e.DB().NewUpdate().Model((*store.Order)(nil)).Set("expires_at = ?", store.Now()-1000).Where("trade_no = ?", expired).Exec(e.Ctx); err != nil {
		t.Fatal(err)
	}

	all := decode[orderList](t, a.Call("GET", "/web-api/admin/orders", nil))
	if all.Total != 4 || all.Items[0].TradeNo != expired || all.Items[0].State != store.OrderExpired || all.Items[0].MerchantName == "" {
		t.Fatalf("订单列表：%+v", all)
	}
	for state, want := range map[string]string{
		store.OrderPaid: paid, store.OrderPending: pending, store.OrderClosed: closed, store.OrderExpired: expired,
	} {
		l := decode[orderList](t, a.Call("GET", "/web-api/admin/orders?state="+state, nil))
		if l.Total != 1 || l.Items[0].TradeNo != want {
			t.Errorf("按状态 %s 筛选：%+v", state, l)
		}
	}
	for q, want := range map[string]int{paid: 1, "P2": 1, "20.00": 1, "10": 1, "nothing": 0} {
		l := decode[orderList](t, a.Call("GET", "/web-api/admin/orders?q="+url.QueryEscape(q), nil))
		if l.Total != want {
			t.Errorf("搜索 %q：%d 条，应为 %d", q, l.Total, want)
		}
	}
	if l := decode[orderList](t, a.Call("GET", "/web-api/admin/orders?merchant_id=9999", nil)); l.Total != 0 {
		t.Error("按商户筛选不对")
	}
	if l := decode[orderList](t, a.Call("GET", fmt.Sprintf("/web-api/admin/orders?from=%d", store.Now()+60000), nil)); l.Total != 0 {
		t.Error("按时间筛选不对")
	}
	if l := decode[orderList](t, a.Call("GET", "/web-api/admin/orders?page=2&page_size=3", nil)); l.Total != 4 || len(l.Items) != 1 {
		t.Errorf("分页不对：%+v", l)
	}

	// 详情：带流水、通知、投递记录。
	e.Deliver()
	detail := decode[struct {
		Order struct {
			store.Order
			State string `json:"state"`
		} `json:"order"`
		Transaction  *store.Transaction    `json:"transaction"`
		Notification *store.Notification   `json:"notification"`
		Attempts     []store.NotifyAttempt `json:"attempts"`
		Anomalies    []store.Anomaly       `json:"anomalies"`
	}](t, a.Call("GET", "/web-api/admin/orders/"+strconv.FormatInt(e.Order(paid).ID, 10), nil))
	if detail.Order.State != store.OrderPaid || detail.Transaction == nil || detail.Notification == nil || detail.Notification.Status != store.NotifySuccess || len(detail.Attempts) != 1 {
		t.Errorf("订单详情：%+v", detail)
	}
	if detail.Anomalies == nil || len(detail.Anomalies) != 0 {
		t.Errorf("没有异常的订单也应返回空数组：%+v", detail.Anomalies)
	}
	emptyDetail := decode[struct {
		Attempts  []store.NotifyAttempt `json:"attempts"`
		Anomalies []store.Anomaly       `json:"anomalies"`
	}](t, a.Call("GET", "/web-api/admin/orders/"+strconv.FormatInt(e.Order(pending).ID, 10), nil))
	if emptyDetail.Attempts == nil || emptyDetail.Anomalies == nil || len(emptyDetail.Attempts) != 0 || len(emptyDetail.Anomalies) != 0 {
		t.Errorf("没有通知和异常时，详情列表必须为 []，不能是 null：%+v", emptyDetail)
	}
	expect(t, a.Call("GET", "/web-api/admin/orders/999999", nil), http.StatusNotFound, "订单不存在")
	expect(t, a.Call("POST", "/web-api/admin/orders/"+strconv.FormatInt(e.Order(paid).ID, 10)+"/close", nil), http.StatusBadRequest, "未支付")
	expect(t, a.Call("POST", "/web-api/admin/orders/999999/close", nil), http.StatusNotFound, "")

	// 导出：UTF-8 带 BOM，防公式注入，状态用中文。
	res := a.Call("GET", "/web-api/admin/orders/export", nil)
	expect(t, res, http.StatusOK, "")
	if !strings.HasPrefix(res.Header.Get("Content-Disposition"), `attachment; filename="orders-`) || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/csv") {
		t.Errorf("导出响应头：%v", res.Header)
	}
	if !strings.HasPrefix(string(res.Body), "\xEF\xBB\xBF") {
		t.Fatal("导出的 CSV 没有 BOM")
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(res.Body), "\xEF\xBB\xBF"))).ReadAll()
	if err != nil || len(rows) != 5 || rows[0][0] != "平台订单号" {
		t.Fatalf("导出内容：%v %v", rows, err)
	}
	byNo := map[string][]string{}
	for _, r := range rows[1:] {
		byNo[r[0]] = r
	}
	if r := byNo[closed]; r[4] != "'=cmd|' /C calc'!A0" || r[8] != "已关闭" {
		t.Errorf("公式注入没处理或状态不对：%v", r)
	}
	if r := byNo[paid]; r[5] != "10.00" || r[7] == "" || r[8] != "已支付" || r[11] == "" || r[12] != "演示渠道" || r[15] != "V1" {
		t.Errorf("已付订单导出：%v", r)
	}
	if r := byNo[expired]; r[8] != "已过期" || r[7] != "" {
		t.Errorf("过期订单导出：%v", r)
	}
	res = a.Call("GET", "/web-api/admin/orders/export?state=paid", nil)
	if rows, _ := csv.NewReader(strings.NewReader(string(res.Body))).ReadAll(); len(rows) != 2 {
		t.Errorf("导出应按筛选条件：%d 行", len(rows))
	}
	if !hasAudit(t, e, "order.close") {
		t.Error("关单没有审计记录")
	}
}

type anomalyList struct {
	Items []struct {
		store.Anomaly
		TradeNo     string             `json:"trade_no"`
		Transaction *store.Transaction `json:"transaction"`
	} `json:"items"`
	Total int `json:"total"`
	Open  int `json:"open"`
}

func TestLedgerAPI(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	m := e.NewMerchant(nil)
	tn := order(t, e, m, "L1", "5", "x")
	ext := e.PayAmount(123) // 唯一待付订单收到少付的款，进入异常

	txns := decode[struct {
		Items    []store.Transaction `json:"items"`
		Total    int                 `json:"total"`
		TradeNos map[string]string   `json:"trade_nos"`
	}](t, a.Call("GET", "/web-api/admin/transactions?state=anomaly", nil))
	if txns.Total != 1 || txns.Items[0].ExternalID != ext || txns.Items[0].Raw != "" {
		t.Fatalf("流水列表（不带原文）：%+v", txns)
	}
	txnID := txns.Items[0].ID
	for q, want := range map[string]int{ext: 1, "1.23": 1, "test***": 1, "9.99": 0} {
		l := decode[struct {
			Total int `json:"total"`
		}](t, a.Call("GET", "/web-api/admin/transactions?q="+url.QueryEscape(q), nil))
		if l.Total != want {
			t.Errorf("流水搜索 %q：%d", q, l.Total)
		}
	}
	if l := decode[struct {
		Total int `json:"total"`
	}](t, a.Call("GET", "/web-api/admin/transactions?direction=out", nil)); l.Total != 0 {
		t.Error("按方向筛选不对")
	}

	an := decode[anomalyList](t, a.Call("GET", "/web-api/admin/anomalies?status=open", nil))
	if an.Total != 1 || an.Open != 1 || an.Items[0].Kind != store.AnomalyUnderpaid || an.Items[0].Transaction == nil || an.Items[0].Transaction.ExternalID != ext {
		t.Fatalf("异常列表：%+v", an)
	}

	// 人工关联：要理由、订单号要对。
	link := fmt.Sprintf("/web-api/admin/transactions/%d/link", txnID)
	expect(t, a.Call("POST", link, map[string]string{"trade_no": tn, "note": "  "}), http.StatusBadRequest, "请填写理由")
	expect(t, a.Call("POST", link, map[string]string{"trade_no": tn, "note": strings.Repeat("长", 501)}), http.StatusBadRequest, "500")
	expect(t, a.Call("POST", link, map[string]string{"trade_no": "123", "note": "x"}), http.StatusBadRequest, "找不到")
	expect(t, a.Call("POST", "/web-api/admin/transactions/999999/link", map[string]string{"trade_no": tn, "note": "x"}), http.StatusBadRequest, "流水不存在")
	expect(t, a.Call("POST", link, map[string]string{"trade_no": tn, "note": "客户少付了，已电话确认"}), http.StatusOK, "")
	if o := e.Order(tn); o.Status != store.OrderPaid || o.PaidAmount != 123 {
		t.Fatalf("关联后订单：%+v", o)
	}
	expect(t, a.Call("POST", link, map[string]string{"trade_no": tn, "note": "x"}), http.StatusBadRequest, "已经关联")
	txns2 := decode[struct {
		TradeNos map[string]string `json:"trade_nos"`
	}](t, a.Call("GET", "/web-api/admin/transactions", nil))
	if txns2.TradeNos[strconv.FormatInt(e.Order(tn).ID, 10)] != tn {
		t.Errorf("流水列表应带出关联的订单号：%v", txns2.TradeNos)
	}

	// 撤销关联：订单变成争议中。
	unlink := fmt.Sprintf("/web-api/admin/orders/%d/unlink", e.Order(tn).ID)
	expect(t, a.Call("POST", unlink, map[string]string{"note": ""}), http.StatusBadRequest, "请填写理由")
	expect(t, a.Call("POST", unlink, map[string]string{"note": "关联错了"}), http.StatusOK, "")
	expect(t, a.Call("POST", unlink, map[string]string{"note": "再来"}), http.StatusBadRequest, "只有已支付")
	expect(t, a.Call("POST", "/web-api/admin/orders/999999/unlink", map[string]string{"note": "x"}), http.StatusNotFound, "")
	if o := e.Order(tn); o.Status != store.OrderDisputed {
		t.Errorf("撤销后订单状态：%s", o.Status)
	}

	// 处理异常。
	an = decode[anomalyList](t, a.Call("GET", "/web-api/admin/anomalies?status=open", nil))
	if an.Open == 0 {
		t.Fatal("撤销关联后应有待处理异常")
	}
	for _, x := range an.Items {
		expect(t, a.Call("POST", fmt.Sprintf("/web-api/admin/anomalies/%d/resolve", x.ID), map[string]string{"note": "已退款"}), http.StatusOK, "")
	}
	expect(t, a.Call("POST", fmt.Sprintf("/web-api/admin/anomalies/%d/resolve", an.Items[0].ID), map[string]string{"note": "x"}), http.StatusBadRequest, "已经处理过")
	if an = decode[anomalyList](t, a.Call("GET", "/web-api/admin/anomalies", nil)); an.Open != 0 {
		t.Errorf("处理完还有 %d 条待处理", an.Open)
	}
	if an = decode[anomalyList](t, a.Call("GET", "/web-api/admin/anomalies?kind=unlinked", nil)); an.Total != 1 || an.Items[0].TradeNo != tn || an.Items[0].ResolvedBy != admin.Actor {
		t.Errorf("按种类筛选异常：%+v", an)
	}

	// 查账状态和"立即查账"（清掉退避）。
	e.Demo.FailWith = fmt.Errorf("网络断了")
	_ = e.App.Pollers.RunOnce(e.Ctx) // 预期失败，下面看失败后的状态
	type statusList struct {
		Items []map[string]any `json:"items"`
	}
	st := decode[statusList](t, a.Call("GET", "/web-api/admin/ledger/status", nil))
	if len(st.Items) != 1 || st.Items[0]["failures"] != float64(1) || st.Items[0]["last_error"] == "" || st.Items[0]["name"] != "演示渠道" || st.Items[0]["source"] != e.ChannelKey() {
		t.Errorf("查账状态：%v", st)
	}
	expect(t, a.Call("POST", "/web-api/admin/ledger/poll", map[string]int64{"channel_id": e.Demo.Row().ID}), http.StatusOK, "")
	st = decode[statusList](t, a.Call("GET", "/web-api/admin/ledger/status", nil))
	if st.Items[0]["backoff_until"] != float64(0) {
		t.Errorf("立即查账应清掉退避：%v", st)
	}
	for _, want := range []string{"ledger.link", "ledger.unlink", "anomaly.resolve"} {
		if !hasAudit(t, e, want) {
			t.Errorf("没有审计记录 %s", want)
		}
	}
}

func TestNotificationsAPI(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	m := e.NewMerchant(nil)
	tn := order(t, e, m, "N1", "1", "x")
	e.Hook.Reply(http.StatusOK, "fail")
	e.Pay(tn)
	e.Deliver()

	l := decode[struct {
		Items []struct {
			store.Notification
			TradeNo    string `json:"trade_no"`
			OutTradeNo string `json:"out_trade_no"`
			MerchantID int64  `json:"merchant_id"`
			NotifyURL  string `json:"notify_url"`
		} `json:"items"`
		Total int `json:"total"`
	}](t, a.Call("GET", "/web-api/admin/notifications?status=pending", nil))
	if l.Total != 1 || l.Items[0].TradeNo != tn || l.Items[0].OutTradeNo != "N1" || l.Items[0].MerchantID != m.ID || l.Items[0].Attempts != 1 || l.Items[0].NotifyURL == "" {
		t.Fatalf("通知列表：%+v", l)
	}
	ov := decode[map[string]any](t, a.Call("GET", "/web-api/admin/overview", nil))
	if ov["retrying_notifications"] != float64(1) {
		t.Errorf("概览里重试中的通知：%v", ov["retrying_notifications"])
	}

	id := e.Order(tn).ID
	res := decode[struct {
		Attempt store.NotifyAttempt `json:"attempt"`
	}](t, a.Call("POST", fmt.Sprintf("/web-api/admin/notifications/%d/resend", id), nil))
	if res.Attempt.Success || !res.Attempt.Manual || res.Attempt.Response != "fail" {
		t.Errorf("手动重发（对方仍回 fail）：%+v", res.Attempt)
	}
	e.Hook.Reply(http.StatusOK, "success")
	res = decode[struct {
		Attempt store.NotifyAttempt `json:"attempt"`
	}](t, a.Call("POST", fmt.Sprintf("/web-api/admin/notifications/%d/resend", id), nil))
	if !res.Attempt.Success {
		t.Errorf("手动重发：%+v", res.Attempt)
	}
	if l := decode[struct {
		Total int `json:"total"`
	}](t, a.Call("GET", "/web-api/admin/notifications?status=success", nil)); l.Total != 1 {
		t.Error("重发成功后应是已送达")
	}
	expect(t, a.Call("POST", "/web-api/admin/notifications/999999/resend", nil), http.StatusBadRequest, "重发失败")
	if !hasAudit(t, e, "notify.resend") {
		t.Error("重发没有审计记录")
	}
}

func TestSettingsAPI(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	type view struct {
		Payment           settings.Payment `json:"payment"`
		Defaults          settings.Payment `json:"defaults"`
		PlatformPublicKey string           `json:"platform_public_key"`
		Demo              bool             `json:"demo"`
		Site              settings.Site    `json:"site"`
	}
	v := decode[view](t, a.Call("GET", "/web-api/admin/settings", nil))
	if v.Payment != settings.DefaultPayment() || v.Defaults != v.Payment || v.PlatformPublicKey == "" || !v.Demo {
		t.Fatalf("初始设置：%+v", v)
	}
	if strings.Contains(string(a.Call("GET", "/web-api/admin/settings", nil).Body), "PRIVATE") {
		t.Error("设置接口不该带出私钥")
	}

	// 收款参数校验。
	bad := settings.DefaultPayment()
	bad.ActiveInterval = 2
	expect(t, a.Call("PUT", "/web-api/admin/settings/payment", bad), http.StatusBadRequest, "活跃查账间隔")
	bad = settings.DefaultPayment()
	bad.OrderTTLSeconds = 30
	expect(t, a.Call("PUT", "/web-api/admin/settings/payment", bad), http.StatusBadRequest, "订单有效期")
	good := settings.DefaultPayment()
	good.OrderTTLSeconds, good.MaxTailCents = 600, 50
	expect(t, a.Call("PUT", "/web-api/admin/settings/payment", good), http.StatusOK, "")
	if v = decode[view](t, a.Call("GET", "/web-api/admin/settings", nil)); v.Payment != good {
		t.Errorf("收款参数没保存：%+v", v.Payment)
	}
	// 新订单用新的有效期。
	m := e.NewMerchant(nil)
	o := e.Order(order(t, e, m, "S1", "1", "x"))
	if ttl := o.ExpiresAt - o.CreatedAt; ttl != 600_000 {
		t.Errorf("新订单有效期 %d 毫秒，应为 600 秒", ttl)
	}

	// 重新生成平台密钥对。
	before := v.PlatformPublicKey
	regen := decode[map[string]string](t, a.Call("POST", "/web-api/admin/settings/platform/regenerate", nil))
	if regen["platform_public_key"] == "" || regen["platform_public_key"] == before {
		t.Errorf("平台密钥没换：%v", regen)
	}

	// 站点名称。
	expect(t, a.Call("PUT", "/web-api/admin/settings/site", map[string]string{"name": " "}), http.StatusBadRequest, "站点名称")
	expect(t, a.Call("PUT", "/web-api/admin/settings/site", map[string]string{"name": " 小店收银 "}), http.StatusOK, "")
	if me := decode[map[string]any](t, a.Call("GET", "/web-api/auth/me", nil)); me["site_name"] != "小店收银" {
		t.Errorf("站点名称：%v", me)
	}
	for _, want := range []string{"settings.payment", "settings.platform_regenerate"} {
		if !hasAudit(t, e, want) {
			t.Errorf("没有审计记录 %s", want)
		}
	}
}

func TestHomeAndEntry(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	type view struct {
		Home  settings.Home `json:"home"`
		Entry struct {
			Enabled bool   `json:"enabled"`
			URL     string `json:"url"`
		} `json:"entry"`
	}
	v := decode[view](t, a.Call("GET", "/web-api/admin/settings", nil))
	if v.Home.Mode != settings.HomeHelp || v.Entry.Enabled || v.Entry.URL != "" {
		t.Fatalf("默认首页是付款人自助、安全入口关着：%+v", v)
	}

	// 首页
	for _, c := range []struct {
		home settings.Home
		want string
	}{
		{settings.Home{Mode: "blog"}, "不认识"},
		{settings.Home{Mode: settings.HomePage, HTML: " \n"}, "HTML"},
		{settings.Home{Mode: settings.HomePage, HTML: strings.Repeat("x", settings.MaxHomeHTML+1)}, "512 KB"},
		{settings.Home{Mode: settings.HomeRedirect, URL: "ftp://example.com"}, "完整网址"},
		{settings.Home{Mode: settings.HomeRedirect, URL: "/help"}, "完整网址"},
		{settings.Home{Mode: settings.HomeRedirect, URL: e.URL + "/"}, "首页自己"},
	} {
		expect(t, a.Call("PUT", "/web-api/admin/settings/home", c.home), http.StatusBadRequest, c.want)
	}
	page := "<!doctype html><title>小林的工作室</title><h1>欢迎</h1>"
	expect(t, a.Call("PUT", "/web-api/admin/settings/home", settings.Home{Mode: settings.HomePage, HTML: page, URL: " https://example.com "}), http.StatusOK, "")
	if r := e.Get("/"); r.Status != 200 || string(r.Body) != page || !strings.HasPrefix(r.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("自定义首页：%d %v %s", r.Status, r.Header, r.Body)
	}
	// 切到跳转时 HTML 留着，换回来不用重贴
	expect(t, a.Call("PUT", "/web-api/admin/settings/home", settings.Home{Mode: settings.HomeRedirect, HTML: page, URL: "https://example.com"}), http.StatusOK, "")
	if r := e.Get("/"); r.Status != http.StatusFound || r.Header.Get("Location") != "https://example.com" {
		t.Fatalf("跳转：%d %v", r.Status, r.Header)
	}
	if v := decode[view](t, a.Call("GET", "/web-api/admin/settings", nil)); v.Home.HTML != page || v.Home.URL != "https://example.com" {
		t.Fatalf("切换方式后另外两项要保留：%+v", v.Home)
	}
	expect(t, a.Call("PUT", "/web-api/admin/settings/home", settings.Home{Mode: settings.HomeNotFound}), http.StatusOK, "")
	if r := e.Get("/"); r.Status != 404 {
		t.Fatalf("首页 404：%d", r.Status)
	}
	if r := e.Get("/no-such-page"); r.Status != 404 {
		t.Fatalf("不存在的路径：%d", r.Status)
	}

	// 安全入口：开启时当前浏览器顺带放行
	res := a.Call("POST", "/web-api/admin/settings/entry", nil)
	expect(t, res, http.StatusOK, "")
	on := decode[view](t, res).Entry
	if !on.Enabled || !strings.HasPrefix(on.URL, e.URL+"/admin?entry=") || !strings.Contains(res.Header.Get("Set-Cookie"), admin.EntryCookie+"=") {
		t.Fatalf("开启安全入口：%+v %v", on, res.Header)
	}
	expect(t, a.Call("GET", "/web-api/admin/settings", nil), http.StatusOK, "")
	// 别的浏览器看不到后台，也试不了密码
	for _, p := range []string{"/login", "/admin", "/admin/orders"} {
		if r := e.Get(p); r.Status != 404 || string(r.Body) != "404 page not found\n" {
			t.Errorf("%s 没入口应 404：%d %s", p, r.Status, r.Body)
		}
	}
	expect(t, raw(e, "POST", "/web-api/auth/login", `{"password":"x"}`, map[string]string{"X-Tillbox": "1"}), http.StatusNotFound, "接口不存在")
	expect(t, e.Get("/web-api/admin/settings"), http.StatusNotFound, "接口不存在")
	// 收银台、付款人自助不受影响
	if r := e.Get("/help"); r.Status != 200 {
		t.Errorf("付款人自助：%d", r.Status)
	}
	// 打开入口链接后放行
	link, _ := url.Parse(on.URL)
	r := e.Get(link.RequestURI())
	if r.Status != http.StatusSeeOther || r.Header.Get("Location") != "/admin" {
		t.Fatalf("入口链接：%d %v", r.Status, r.Header)
	}
	cookie := r.Header.Get("Set-Cookie")
	cookie = cookie[:strings.Index(cookie, ";")]
	expect(t, raw(e, "POST", "/web-api/auth/login", `{"password":"x"}`, map[string]string{"X-Tillbox": "1", "Cookie": cookie}), http.StatusUnauthorized, "密码不对")

	// 换链接：旧的入口 cookie 失效，当前浏览器拿到新的
	again := decode[view](t, a.Call("POST", "/web-api/admin/settings/entry", nil)).Entry
	if again.URL == on.URL {
		t.Fatal("换链接后应是新的")
	}
	expect(t, raw(e, "GET", "/web-api/auth/me", "", map[string]string{"Cookie": cookie}), http.StatusNotFound, "")
	expect(t, a.Call("GET", "/web-api/admin/settings", nil), http.StatusOK, "")

	// 命令行直接改库也马上生效（不走缓存）
	other := settings.New(e.DB(), e.App.Settings.Box())
	if _, err := other.ResetEntry(e.Ctx); err != nil {
		t.Fatal(err)
	}
	expect(t, a.Call("GET", "/web-api/admin/settings", nil), http.StatusNotFound, "")
	if err := other.DisableEntry(e.Ctx); err != nil {
		t.Fatal(err)
	}
	expect(t, a.Call("GET", "/web-api/admin/settings", nil), http.StatusOK, "")

	expect(t, a.Call("POST", "/web-api/admin/settings/entry", nil), http.StatusOK, "")
	expect(t, a.Call("DELETE", "/web-api/admin/settings/entry", nil), http.StatusOK, "")
	if r := e.Get("/login"); r.Status != 200 {
		t.Errorf("关闭后登录页恢复：%d", r.Status)
	}
	for _, want := range []string{"settings.home", "settings.entry", "settings.entry_off"} {
		if !hasAudit(t, e, want) {
			t.Errorf("没有审计记录 %s", want)
		}
	}
}

func TestOverview(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	m1 := e.NewMerchant(nil)
	m2 := e.NewMerchant(nil)
	e.Pay(order(t, e, m1, "O1", "10", "x"))
	e.Pay(order(t, e, m1, "O2", "5.5", "x"))
	e.Pay(order(t, e, m2, "O3", "20", "x"))
	order(t, e, m2, "O4", "1", "x")
	e.PayAmount(777)

	type day struct {
		Date   string `json:"date"`
		Count  int64  `json:"count"`
		Amount int64  `json:"amount"`
	}
	ov := decode[struct {
		Today     day   `json:"today"`
		Days      []day `json:"days"`
		Merchants []struct {
			MerchantID int64  `json:"merchant_id"`
			Name       string `json:"name"`
			Count      int64  `json:"count"`
			Amount     int64  `json:"amount"`
			Days       []day  `json:"days"`
		} `json:"merchants"`
		Pending   int64            `json:"pending_orders"`
		Anomalies int64            `json:"open_anomalies"`
		Ledgers   []map[string]any `json:"ledgers"`
	}](t, a.Call("GET", "/web-api/admin/overview", nil))
	today := time.Now().In(epay.CST).Format(time.DateOnly)
	if ov.Today.Date != today || ov.Today.Count != 3 || ov.Today.Amount != 3550 || len(ov.Days) != 7 || ov.Days[6] != ov.Today {
		t.Errorf("今日收款：%+v（共 %d 天）", ov.Today, len(ov.Days))
	}
	if len(ov.Merchants) != 2 || ov.Merchants[0].MerchantID != m2.ID || ov.Merchants[0].Amount != 2000 || ov.Merchants[1].Count != 2 || len(ov.Merchants[1].Days) != 7 {
		t.Errorf("按商户：%+v", ov.Merchants)
	}
	if ov.Pending != 1 || ov.Anomalies != 1 || len(ov.Ledgers) != 1 || ov.Ledgers[0]["name"] != "演示渠道" {
		t.Errorf("待处理：%d 待付，%d 异常", ov.Pending, ov.Anomalies)
	}
	// 停用的渠道不查账，不出现在概览里；流水页的查账状态仍列出它，标明停用。
	decode[map[string]store.Channel](t, a.Call("POST", "/web-api/admin/channels", map[string]any{"type": store.ChannelDemo, "name": "停用的演示", "enabled": false}))
	ledgers := decode[struct {
		Ledgers []map[string]any `json:"ledgers"`
	}](t, a.Call("GET", "/web-api/admin/overview", nil)).Ledgers
	if len(ledgers) != 1 || ledgers[0]["name"] != "演示渠道" || ledgers[0]["enabled"] != true {
		t.Errorf("概览只列启用的渠道：%v", ledgers)
	}
	st := decode[struct {
		Items []map[string]any `json:"items"`
	}](t, a.Call("GET", "/web-api/admin/ledger/status", nil)).Items
	if len(st) != 2 || st[1]["name"] != "停用的演示" || st[1]["enabled"] != false {
		t.Errorf("查账状态应列出停用的渠道：%v", st)
	}
	month := decode[struct {
		Days []day `json:"days"`
	}](t, a.Call("GET", "/web-api/admin/overview?days=30", nil))
	if len(month.Days) != 30 || month.Days[29].Date != today {
		t.Errorf("近 30 天：%d 天", len(month.Days))
	}
	if odd := decode[struct {
		Days []day `json:"days"`
	}](t, a.Call("GET", "/web-api/admin/overview?days=365", nil)); len(odd.Days) != 7 {
		t.Error("只支持 7 天和 30 天")
	}
}

func TestUnknownAPI(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	expect(t, a.Call("GET", "/web-api/admin/nothing", nil), http.StatusNotFound, "接口不存在")
	expect(t, a.Call("DELETE", "/web-api/admin/orders", nil), http.StatusNotFound, "接口不存在")
}

// 带 If-Match 保存时，版本和库里的对不上返回 409，不覆盖；不带时照常保存（脚本、旧页面）。
func TestSaveConflict(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	type revs struct {
		Revisions map[string]int64 `json:"revisions"`
	}
	rev := decode[revs](t, a.Call("GET", "/web-api/admin/settings", nil)).Revisions["payment"]
	if rev != 0 {
		t.Fatalf("没存过的设置版本应为 0，实际 %d", rev)
	}
	p := settings.DefaultPayment()
	p.OrderTTLSeconds = 600
	r1 := decode[struct {
		Revision int64 `json:"revision"`
	}](t, a.CallIf("PUT", "/web-api/admin/settings/payment", p, rev)).Revision
	if r1 <= 0 {
		t.Fatalf("保存后应返回新版本，实际 %d", r1)
	}
	if got := decode[revs](t, a.Call("GET", "/web-api/admin/settings", nil)).Revisions["payment"]; got != r1 {
		t.Errorf("读到的版本 %d 和保存返回的 %d 不一致", got, r1)
	}
	// 另一个标签页还拿着旧版本。
	stale := settings.DefaultPayment()
	stale.OrderTTLSeconds = 900
	expect(t, a.CallIf("PUT", "/web-api/admin/settings/payment", stale, rev), http.StatusConflict, "别处改过")
	if cur, _ := e.App.Settings.Payment(e.Ctx); cur.OrderTTLSeconds != 600 {
		t.Fatalf("冲突时不能覆盖，实际有效期 %d", cur.OrderTTLSeconds)
	}
	expect(t, a.CallIf("PUT", "/web-api/admin/settings/payment", stale, r1), http.StatusOK, "")
	expect(t, a.Call("PUT", "/web-api/admin/settings/payment", p), http.StatusOK, "")
	expect(t, a.CallWith("PUT", "/web-api/admin/settings/payment", p, map[string]string{"If-Match": "abc"}), http.StatusBadRequest, "If-Match")

	// 归档保留期和自动备份在“备份与归档”页，同样检查。
	type ops struct {
		RetentionRevision int64 `json:"retention_revision"`
		Backup            struct {
			Revision int64           `json:"revision"`
			Config   settings.Backup `json:"config"`
		} `json:"backup"`
	}
	o := decode[ops](t, a.Call("GET", "/web-api/admin/operations", nil))
	ret, _ := e.App.Settings.Retention(e.Ctx)
	ret.RawDays = 120
	expect(t, a.CallIf("PUT", "/web-api/admin/operations/retention", ret, o.RetentionRevision), http.StatusOK, "")
	expect(t, a.CallIf("PUT", "/web-api/admin/operations/retention", ret, o.RetentionRevision), http.StatusConflict, "")
	if o.Backup.Config.Enabled || o.Backup.Config.Keep != 7 {
		t.Errorf("自动备份默认关、留 7 份：%+v", o.Backup.Config)
	}
	if e.App.Backup.Supported() {
		expect(t, a.CallIf("PUT", "/web-api/admin/operations/backup", settings.Backup{Enabled: true, Keep: 0}, o.Backup.Revision), http.StatusBadRequest, "1–90")
		if files, _ := e.App.Backup.List(); len(files) != 0 {
			t.Fatalf("测试开始前不该有备份：%v", files)
		}
		// 打开时到期就马上备份一份，不用等下一轮。
		expect(t, a.CallIf("PUT", "/web-api/admin/operations/backup", settings.Backup{Enabled: true, Keep: 3}, o.Backup.Revision), http.StatusOK, "")
		if files, _ := e.App.Backup.List(); len(files) != 1 {
			t.Errorf("打开自动备份后应马上备份一份，实际 %d 份", len(files))
		}
		expect(t, a.CallIf("PUT", "/web-api/admin/operations/backup", settings.Backup{Enabled: false, Keep: 3}, o.Backup.Revision), http.StatusConflict, "")
		// 当天关掉再打开，不重复备份。
		expect(t, a.Call("PUT", "/web-api/admin/operations/backup", settings.Backup{Enabled: false, Keep: 3}), http.StatusOK, "")
		expect(t, a.Call("PUT", "/web-api/admin/operations/backup", settings.Backup{Enabled: true, Keep: 3}), http.StatusOK, "")
		if files, _ := e.App.Backup.List(); len(files) != 1 {
			t.Errorf("当天重新打开不该再备份，实际 %d 份", len(files))
		}
	} else {
		expect(t, a.CallIf("PUT", "/web-api/admin/operations/backup", settings.Backup{Enabled: true, Keep: 3}, o.Backup.Revision), http.StatusBadRequest, "SQLite")
	}

	// 商户：版本是修改时间；重置密钥也算修改，返回新的修改时间。
	m := decode[struct {
		Merchant store.Merchant `json:"merchant"`
	}](t, a.Call("POST", "/web-api/admin/merchants", merchant.Input{Name: "商城", Enabled: true})).Merchant
	path := fmt.Sprintf("/web-api/admin/merchants/%d", m.ID)
	saved := decode[struct {
		Merchant store.Merchant `json:"merchant"`
	}](t, a.CallIf("PUT", path, merchant.Input{Name: "商城二", Enabled: true}, m.UpdatedAt)).Merchant
	if saved.UpdatedAt <= m.UpdatedAt {
		t.Errorf("保存后修改时间应变大：%d → %d", m.UpdatedAt, saved.UpdatedAt)
	}
	expect(t, a.CallIf("PUT", path, merchant.Input{Name: "商城三", Enabled: true}, m.UpdatedAt), http.StatusConflict, "别处改过")
	reset := decode[struct {
		UpdatedAt int64 `json:"updated_at"`
	}](t, a.Call("POST", path+"/reset-key", nil))
	expect(t, a.CallIf("PUT", path, merchant.Input{Name: "商城三", Enabled: true}, saved.UpdatedAt), http.StatusConflict, "")
	expect(t, a.CallIf("PUT", path, merchant.Input{Name: "商城三", Enabled: true}, reset.UpdatedAt), http.StatusOK, "")

	// 渠道。
	_, ch, _ := e.App.Channels.Get(store.ChannelID(e.ChannelKey()))
	cpath := fmt.Sprintf("/web-api/admin/channels/%d", ch.ID)
	in := map[string]any{"name": "演示渠道二", "enabled": true, "config": map[string]any{}}
	expect(t, a.CallIf("PUT", cpath, in, ch.UpdatedAt), http.StatusOK, "")
	expect(t, a.CallIf("PUT", cpath, in, ch.UpdatedAt), http.StatusConflict, "别处改过")
}
