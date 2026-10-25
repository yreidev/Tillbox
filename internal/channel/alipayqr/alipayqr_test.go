package alipayqr_test

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/channel"
	"tillbox/internal/channel/alipayqr"
	"tillbox/internal/channel/registry"
	"tillbox/internal/epay"
	"tillbox/internal/store"
)

type item struct {
	id       string
	at       time.Time
	amount   string
	dir      string
	override map[string]any // 覆盖返回的字段，用来造格式不对的流水
}

// fakeAlipay 是本地的支付宝开放平台：校验请求签名，按时间窗口返回账务明细，给响应签名。
type fakeAlipay struct {
	t        *testing.T
	appID    string
	appPub   *rsa.PublicKey
	signKey  *rsa.PrivateKey // 给响应签名的私钥（正常时是支付宝私钥）
	limit    int             // 每页最多返回几条
	numeric  bool            // 分页字段用数字返回
	fail     func(w http.ResponseWriter) bool
	mu       sync.Mutex
	items    []item
	requests []string
}

func (f *fakeAlipay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.RequestURI)
	f.mu.Unlock()
	if r.URL.Path != "/v3/alipay/data/bill/accountlog/query" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("alipay-request-id") == "" {
		f.t.Error("请求没带 alipay-request-id")
	}
	// 校验请求签名：认证串\n方法\nURI\n请求体\n。
	authz := r.Header.Get("Authorization")
	auth, sign, ok := strings.Cut(strings.TrimPrefix(authz, "ALIPAY-SHA256withRSA "), ",sign=")
	if !ok || !strings.HasPrefix(auth, "app_id="+f.appID+",nonce=") || !strings.Contains(auth, ",timestamp=") {
		f.t.Errorf("Authorization 格式不对：%s", authz)
	}
	if !epay.RSAVerify(auth+"\nGET\n"+r.RequestURI+"\n\n", sign, f.appPub) {
		f.t.Errorf("请求签名不对：%s", r.RequestURI)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if strings.Contains(r.RequestURI, "+") || strings.Contains(r.RequestURI, " ") {
		f.t.Errorf("查询串的空格应编码成 %%20：%s", r.RequestURI)
	}
	if f.fail != nil && f.fail(w) {
		return
	}
	q := r.URL.Query()
	start, err1 := time.ParseInLocation(time.DateTime, q.Get("start_time"), epay.CST)
	end, err2 := time.ParseInLocation(time.DateTime, q.Get("end_time"), epay.CST)
	page, _ := strconv.Atoi(q.Get("page_no"))
	if err1 != nil || err2 != nil || page < 1 || q.Get("page_size") != "2000" {
		f.t.Errorf("查询参数不对：%v", q)
	}
	var hits []item
	f.mu.Lock()
	for _, it := range f.items {
		if !it.at.Before(start) && it.at.Before(end) {
			hits = append(hits, it)
		}
	}
	f.mu.Unlock()
	sort.Slice(hits, func(i, j int) bool { return hits[i].id < hits[j].id })
	from := min((page-1)*f.limit, len(hits))
	to := min(from+f.limit, len(hits))
	list := []map[string]any{}
	for _, it := range hits[from:to] {
		m := map[string]any{
			"account_log_id": it.id, "alipay_order_no": "2026" + it.id, "trans_dt": it.at.In(epay.CST).Format(time.DateTime),
			"trans_amount": it.amount, "direction": it.dir, "other_account": "张*(zha***@example.com)", "type": "在线支付",
			"complement_info": map[string]any{"k": "v"}, "balance": 12.5,
		}
		for k, v := range it.override {
			m[k] = v
		}
		list = append(list, m)
	}
	res := map[string]any{"detail_list": list}
	if f.numeric {
		res["page_no"], res["page_size"], res["total_size"] = page, f.limit, len(hits)
	} else {
		res["page_no"], res["page_size"], res["total_size"] = strconv.Itoa(page), strconv.Itoa(f.limit), strconv.Itoa(len(hits))
	}
	body, _ := json.Marshal(res)
	f.sign(w, body)
	_, _ = w.Write(body)
}

func (f *fakeAlipay) sign(w http.ResponseWriter, body []byte) {
	ts, nonce := strconv.FormatInt(time.Now().UnixMilli(), 10), "n"+strconv.FormatInt(time.Now().UnixNano(), 10)
	sig, _ := epay.RSASign(ts+"\n"+nonce+"\n"+string(body)+"\n", f.signKey)
	w.Header().Set("alipay-timestamp", ts)
	w.Header().Set("alipay-nonce", nonce)
	w.Header().Set("alipay-signature", sig)
	w.Header().Set("Content-Type", "application/json")
}

func setup(t *testing.T) (*apptest.Env, *fakeAlipay, *alipayqr.Channel) {
	e := apptest.New(t)
	appPriv, appPub, _ := epay.GenerateKeyPair()
	aliPriv, aliPub, _ := epay.GenerateKeyPair()
	ap, _ := epay.ParsePublicKey(appPub)
	sk, _ := epay.ParsePrivateKey(aliPriv)
	f := &fakeAlipay{t: t, appID: "2021000000000001", appPub: ap, signKey: sk, limit: 2000}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg, _ := json.Marshal(alipayqr.Input{
		AppID: f.appID, AppPrivateKey: appPriv, AlipayPublicKey: aliPub,
		Gateway: srv.URL, QRCodeURL: "https://qr.alipay.com/fkx00000000000000000000",
	})
	row, err := e.App.Channels.Create(e.Ctx, registry.Input{Type: store.ChannelAlipayQR, Name: "经营码", Enabled: true, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if v := alipayqr.ViewOf(row.Config); v.AppPublicKey != appPub || !v.HasPrivateKey || strings.Contains(row.Config, "PRIVATE KEY") {
		t.Fatalf("应用私钥应加密保存，并算出应用公钥：%+v", v)
	}
	inst, _, _ := e.App.Channels.Get(row.ID)
	ch := inst.(*alipayqr.Channel)
	ch.SetHTTPClient(srv.Client())
	return e, f, ch
}

// update 用后台同样的方式改经营码渠道（私钥不填表示保留）。
func update(t *testing.T, e *apptest.Env, ch *alipayqr.Channel, enabled bool, edit func(*alipayqr.Input)) {
	t.Helper()
	cfg, err := ch.Config()
	if err != nil {
		t.Fatal(err)
	}
	in := alipayqr.Input{AppID: cfg.AppID, AlipayPublicKey: cfg.AlipayPublicKey, Gateway: cfg.Gateway, QRCodeURL: cfg.QRCodeURL, PayeeName: cfg.PayeeName}
	edit(&in)
	raw, _ := json.Marshal(in)
	row := ch.Row()
	if _, err := e.App.Channels.Update(e.Ctx, row.ID, registry.Input{Name: row.Name, Enabled: enabled, Config: raw}, -1); err != nil {
		t.Fatal(err)
	}
}

var base = time.Date(2026, 10, 15, 12, 0, 0, 0, epay.CST)

func TestFetchSplitsWindow(t *testing.T) {
	_, f, ch := setup(t)
	f.limit = 3
	for i := range 9 {
		f.items = append(f.items, item{id: fmt.Sprintf("A%03d", i), at: base.Add(time.Duration(i) * 7 * time.Minute), amount: "10.0" + strconv.Itoa(i%10), dir: "收入"})
	}
	// 窗口外的不该拿到。
	f.items = append(f.items, item{id: "OUT1", at: base.Add(-time.Second), amount: "1.00", dir: "收入"}, item{id: "OUT2", at: base.Add(time.Hour), amount: "1.00", dir: "收入"})

	got, err := ch.Fetch(context.Background(), base, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, e := range got {
		if ids[e.ExternalID] {
			t.Errorf("重复：%s", e.ExternalID)
		}
		ids[e.ExternalID] = true
	}
	if len(got) != 9 || ids["OUT1"] || ids["OUT2"] {
		t.Errorf("应拿到窗口内 9 条，实际 %d：%v", len(got), ids)
	}
	if len(f.requests) < 4 {
		t.Errorf("一页装不下应切窗，实际只请求了 %d 次", len(f.requests))
	}
	for _, uri := range f.requests {
		if strings.Contains(uri, "page_no=2") {
			t.Errorf("能切窗时不该翻页：%s", uri)
		}
	}
	e := got[0]
	if e.Amount != 1000 || e.Direction != store.DirIn || !e.OccurredAt.Equal(base) || e.AlipayOrderNo != "2026A000" || e.OtherAccount == "" || e.Raw == "" {
		t.Errorf("流水字段不对：%+v", e)
	}
}

func TestFetchPagesWithinOneSecond(t *testing.T) {
	_, f, ch := setup(t)
	f.limit = 2
	f.numeric = true
	for i := range 5 {
		f.items = append(f.items, item{id: fmt.Sprintf("S%d", i), at: base, amount: "1.00", dir: "收入"})
	}
	got, err := ch.Fetch(context.Background(), base, base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Errorf("同一秒内 5 条应翻页拿全，实际 %d", len(got))
	}
}

func TestDirectionAndAmount(t *testing.T) {
	_, f, ch := setup(t)
	f.items = []item{
		{id: "O1", at: base, amount: "-5.20", dir: "支出"},
		{id: "O2", at: base, amount: "-0.01", dir: ""},
		{id: "I1", at: base, amount: "0.5", dir: "收入"},
	}
	got, err := ch.Fetch(context.Background(), base, base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		dir    string
		amount int64
	}{"O1": {store.DirOut, 520}, "O2": {store.DirOut, 1}, "I1": {store.DirIn, 50}}
	for _, e := range got {
		w := want[e.ExternalID]
		if e.Direction != w.dir || e.Amount != w.amount {
			t.Errorf("%s：%s %d，应为 %s %d", e.ExternalID, e.Direction, e.Amount, w.dir, w.amount)
		}
	}
}

func TestFetchKeepsInvalidItems(t *testing.T) {
	_, f, ch := setup(t)
	f.items = []item{
		{id: "I1", at: base, amount: "1.00", dir: "收入"},
		{id: "I2", at: base, amount: "2.00", dir: "收入", override: map[string]any{"trans_dt": "2026-02-30 10:00:00"}},
		{id: "I3", at: base, amount: "3.001", dir: "收入"},
		{id: "I4", at: base, amount: "4.00", dir: "收入", override: map[string]any{"account_log_id": nil, "trans_amount": true}},
	}
	got, err := ch.Fetch(context.Background(), base, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("一条流水解析不了不该让整段查账失败：%v", err)
	}
	if len(got) != 4 {
		t.Fatalf("应返回 4 条，实际 %d", len(got))
	}
	byID := map[string]channel.Entry{}
	for _, e := range got {
		byID[e.ExternalID] = e
	}
	if e := byID["I1"]; e.Invalid != "" || e.Amount != 100 {
		t.Errorf("正常流水不受影响：%+v", e)
	}
	if e := byID["I2"]; !strings.Contains(e.Invalid, "时间") || e.Amount != 200 || !e.OccurredAt.IsZero() || e.Raw == "" {
		t.Errorf("时间不对的流水应带原因和读得出的金额：%+v", e)
	}
	if e := byID["I3"]; !strings.Contains(e.Invalid, "金额") || e.Amount != 0 || !e.OccurredAt.Equal(base) {
		t.Errorf("金额不对的流水应带原因和读得出的时间：%+v", e)
	}
	var unknown channel.Entry
	for id, e := range byID {
		if strings.HasPrefix(id, "invalid:") {
			unknown = e
		}
	}
	if unknown.Invalid == "" || !strings.Contains(unknown.Raw, `"trans_amount":true`) {
		t.Errorf("读不了且没有流水号的一条应按原文摘要记下：%+v", byID)
	}
}

func TestResponseSignature(t *testing.T) {
	_, f, ch := setup(t)
	other, _, _ := epay.GenerateKeyPair()
	f.signKey, _ = epay.ParsePrivateKey(other)
	_, err := ch.Fetch(context.Background(), base, base.Add(time.Minute))
	if err == nil || !strings.Contains(err.Error(), "验签失败") {
		t.Errorf("响应签名不对时应报错：%v", err)
	}
	f.fail = func(w http.ResponseWriter) bool {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"detail_list":[]}`))
		return true
	}
	if _, err := ch.Fetch(context.Background(), base, base.Add(time.Minute)); err == nil {
		t.Error("没有签名的响应应当拒绝")
	}
}

func TestErrors(t *testing.T) {
	e, f, ch := setup(t)
	reply := func(status int, code string, h map[string]string) func(w http.ResponseWriter) bool {
		return func(w http.ResponseWriter) bool {
			body := []byte(`{"code":"` + code + `","message":"出错了"}`)
			f.sign(w, body)
			for k, v := range h {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return true
		}
	}

	f.fail = reply(http.StatusUnauthorized, "invalid-auth", nil)
	_, err := ch.Fetch(e.Ctx, base, base.Add(time.Minute))
	if !errors.Is(err, channel.ErrAuth) {
		t.Errorf("401 应归为凭据错误：%v", err)
	}
	f.fail = reply(http.StatusBadRequest, "INSUFFICIENT_PERMISSION", nil)
	if _, err = ch.Fetch(e.Ctx, base, base.Add(time.Minute)); !errors.Is(err, channel.ErrAuth) {
		t.Errorf("权限不足应归为凭据错误：%v", err)
	}
	f.fail = reply(http.StatusTooManyRequests, "", map[string]string{"Retry-After": "120"})
	_, err = ch.Fetch(e.Ctx, base, base.Add(time.Minute))
	var ra *channel.RetryAfterError
	if !errors.As(err, &ra) || ra.After != 2*time.Minute {
		t.Errorf("429 应按 Retry-After 退避：%v", err)
	}
	f.fail = reply(http.StatusBadRequest, "INVALID_PARAMETER", nil)
	_, err = ch.Fetch(e.Ctx, base, base.Add(time.Minute))
	var api *alipayqr.APIError
	if !errors.As(err, &api) || api.Code != "INVALID_PARAMETER" || errors.Is(err, channel.ErrAuth) {
		t.Errorf("其他错误应原样返回：%v", err)
	}
	// 错误响应带了签名但验不过：当作不可信。
	f.fail = func(w http.ResponseWriter) bool {
		w.Header().Set("alipay-signature", "bad")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"X"}`))
		return true
	}
	if _, err = ch.Fetch(e.Ctx, base, base.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "验签失败") {
		t.Errorf("错误响应验签失败：%v", err)
	}

	// 测试连接走同一套。
	f.fail = nil
	if err := ch.Test(e.Ctx); err != nil {
		t.Errorf("测试连接：%v", err)
	}
}

func TestReadyAndConfig(t *testing.T) {
	e, _, ch := setup(t)
	if err := ch.Ready(e.Ctx); err != nil {
		t.Errorf("配置齐全时应就绪：%v", err)
	}
	update(t, e, ch, false, func(*alipayqr.Input) {})
	if err := ch.Ready(e.Ctx); err == nil || !strings.Contains(err.Error(), "停用") {
		t.Errorf("停用后不该就绪：%v", err)
	}
	update(t, e, ch, false, func(in *alipayqr.Input) { in.AlipayPublicKey = "" })
	if _, err := ch.Fetch(e.Ctx, base, base.Add(time.Minute)); !errors.Is(err, channel.ErrNotConfigured) {
		t.Errorf("缺参数时应报未配置：%v", err)
	}
	if cfg, _ := ch.Config(); cfg.AppPrivateKey == "" {
		t.Error("没填私钥时应保留原来的私钥")
	}
	g, _ := ch.Guide(e.Ctx, nil)
	if g.QRCode == "" || !g.ManualAmount {
		t.Errorf("收款指引：%+v", g)
	}
}

func TestParseYuan(t *testing.T) {
	ok := map[string]int64{"10": 1000, "10.5": 1050, "10.05": 1005, "-0.01": -1, "+3.00": 300, " 7.1 ": 710, "0": 0}
	for s, want := range ok {
		if got, err := alipayqr.ParseYuan(s); err != nil || got != want {
			t.Errorf("ParseYuan(%q) = %d, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "1.234", "abc", "1,000", ".5", "1e3"} {
		if _, err := alipayqr.ParseYuan(s); err == nil {
			t.Errorf("ParseYuan(%q) 应报错", s)
		}
	}
}
