package monitor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/assistance"
	"tillbox/internal/epay"
	"tillbox/internal/ledger"
	"tillbox/internal/monitor"
	"tillbox/internal/order"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

const token = "123456:test-token"

type sent struct {
	ChatID    string
	Text      string
	ParseMode string
	Markup    string // 消息下方按钮的 JSON
}

// fakeTelegram 是假的 Bot API：记下发出的消息，getUpdates 依次返回排好的批次，没有时挂起一会儿再返回空。
type fakeTelegram struct {
	srv *httptest.Server

	mu       sync.Mutex
	sent     []sent
	sendFail bool     // sendMessage 返回 403
	updates  []string // 每项是一批 getUpdates 的 result（JSON 数组）
	conflict bool     // getUpdates 返回 409（机器人设了 Webhook）
	botID    int64
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{botID: 123456}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (f *fakeTelegram) serve(w http.ResponseWriter, r *http.Request) {
	tok, method, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
	if tok != token {
		reply(w, 401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	_ = r.ParseMultipartForm(1 << 20)
	f.mu.Lock()
	switch method {
	case "getMe":
		id := f.botID
		f.mu.Unlock()
		reply(w, 200, fmt.Sprintf(`{"ok":true,"result":{"id":%d,"is_bot":true,"first_name":"Tillbox","username":"tillbox_test_bot"}}`, id))
	case "sendMessage":
		f.sent = append(f.sent, sent{r.FormValue("chat_id"), r.FormValue("text"), r.FormValue("parse_mode"), r.FormValue("reply_markup")})
		fail := f.sendFail
		f.mu.Unlock()
		if fail {
			reply(w, 403, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
			return
		}
		reply(w, 200, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"private"}}}`)
	case "getUpdates":
		if f.conflict {
			f.mu.Unlock()
			reply(w, 409, `{"ok":false,"error_code":409,"description":"Conflict: can't use getUpdates method while webhook is active"}`)
			return
		}
		if len(f.updates) > 0 {
			batch := f.updates[0]
			f.updates = f.updates[1:]
			f.mu.Unlock()
			reply(w, 200, `{"ok":true,"result":`+batch+`}`)
			return
		}
		f.mu.Unlock()
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
		reply(w, 200, `{"ok":true,"result":[]}`)
	default:
		f.mu.Unlock()
		reply(w, 404, `{"ok":false,"error_code":404,"description":"Not Found"}`)
	}
}

func (f *fakeTelegram) messages() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.sent...)
}

func (f *fakeTelegram) reset() {
	f.mu.Lock()
	f.sent = nil
	f.mu.Unlock()
}

// clock 是可以拨动的“现在”。
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T) (*apptest.Env, *monitor.Service, *fakeTelegram, *clock) {
	t.Helper()
	e := apptest.New(t)
	f := newFakeTelegram(t)
	c := &clock{t: time.Now()}
	m := e.App.Monitor
	m.Now, m.Checker.Now, m.Telegram.Now = c.Now, c.Now, c.Now
	m.Telegram.ServerURL = f.srv.URL
	m.PublicURL = "https://pay.example.com"
	return e, m, f, c
}

func configure(t *testing.T, e *apptest.Env, m *monitor.Service, chatIDs ...int64) {
	t.Helper()
	if _, err := m.Telegram.Configure(e.Ctx, token, monitor.Prefs{RemindHours: 1, Paid: true, Claims: true}); err != nil {
		t.Fatal(err)
	}
	for _, id := range chatIDs {
		if _, err := e.DB().NewInsert().Model(&store.TelegramChat{ChatID: id, Name: "管理员", CreatedAt: 1}).Exec(e.Ctx); err != nil {
			t.Fatal(err)
		}
	}
}

var cases atomic.Int64

func addCase(t *testing.T, e *apptest.Env) int64 {
	t.Helper()
	c := store.AssistanceCase{Source: "payer", Kind: "missing", Status: "manual", DedupeKey: fmt.Sprint("test-", cases.Add(1)), CreatedAt: 1, UpdatedAt: 1}
	if _, err := e.DB().NewInsert().Model(&c).Exec(e.Ctx); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func evaluate(t *testing.T, e *apptest.Env, m *monitor.Service) {
	t.Helper()
	if err := m.Evaluate(e.Ctx); err != nil {
		t.Fatal(err)
	}
}

func openAlerts(t *testing.T, e *apptest.Env, m *monitor.Service) []store.Alert {
	t.Helper()
	as, err := m.Open(e.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	return as
}

// 告警的一生：出现时通知一次，笔数变多再通知，持续未恢复按间隔提醒，消失一段时间后算恢复并通知。
func TestAlertLifecycle(t *testing.T) {
	e, m, f, c := setup(t)
	configure(t, e, m, 42, 43)

	evaluate(t, e, m)
	if as := openAlerts(t, e, m); len(as) != 0 {
		t.Fatalf("没有问题时不应有告警：%+v", as)
	}
	if got := f.messages(); len(got) != 0 {
		t.Fatalf("没有问题时不应发消息：%+v", got)
	}

	first := addCase(t, e)
	evaluate(t, e, m)
	as := openAlerts(t, e, m)
	if len(as) != 1 || as[0].Key != "assistance_manual" || as[0].Count != 1 || as[0].NotifiedAt == 0 || as[0].Link != "/admin/assistance" {
		t.Fatalf("应开一条告警：%+v", as)
	}
	got := f.messages()
	if len(got) != 2 || got[0].ChatID != "42" || got[1].ChatID != "43" {
		t.Fatalf("每个绑定的会话各发一条：%+v", got)
	}
	for _, want := range []string{"🟠 <b>1 个付款问题等人工处理</b>", "<blockquote>AI 核查后无法自动确认", "<i>Tillbox · "} {
		if !strings.Contains(got[0].Text, want) {
			t.Errorf("消息里应有 %q：\n%s", want, got[0].Text)
		}
	}
	if got[0].ParseMode != "HTML" || !strings.Contains(got[0].Markup, `"text":"去处理"`) || !strings.Contains(got[0].Markup, `"url":"https://pay.example.com/admin/assistance"`) {
		t.Errorf("用 HTML 格式，只有一条告警时按钮直接去处理：%+v", got[0])
	}

	// 没有变化：不再发。
	f.reset()
	c.Add(monitor.Every)
	evaluate(t, e, m)
	if got := f.messages(); len(got) != 0 {
		t.Fatalf("没有变化不应再发：%+v", got)
	}

	// 笔数变多：再发一次。
	second := addCase(t, e)
	c.Add(monitor.Every)
	evaluate(t, e, m)
	if got := f.messages(); len(got) != 2 || !strings.Contains(got[0].Text, "2 个付款问题等人工处理（有新增）") {
		t.Fatalf("有新增时应再通知：%+v", got)
	}

	// 超过再提醒间隔（1 小时）还没恢复：提醒。
	f.reset()
	c.Add(time.Hour)
	evaluate(t, e, m)
	if got := f.messages(); len(got) != 2 || !strings.Contains(got[0].Text, "⏰ <b>2 个付款问题等人工处理</b>") || !strings.Contains(got[0].Text, "已持续 1 小时，还没恢复") {
		t.Fatalf("持续未恢复应提醒：%+v", got)
	}

	// 处理完：先不算恢复，问题消失 2 分钟后才算。
	f.reset()
	if _, err := e.DB().NewUpdate().Model((*store.AssistanceCase)(nil)).Set("status = 'closed'").Where("id IN (?, ?)", first, second).Exec(e.Ctx); err != nil {
		t.Fatal(err)
	}
	c.Add(monitor.Every)
	evaluate(t, e, m)
	if as := openAlerts(t, e, m); len(as) != 1 {
		t.Fatalf("刚消失时告警还在：%+v", as)
	}
	c.Add(monitor.ResolveAfter)
	evaluate(t, e, m)
	if as := openAlerts(t, e, m); len(as) != 0 {
		t.Fatalf("消失 2 分钟后应恢复：%+v", as)
	}
	if got := f.messages(); len(got) != 2 || !strings.Contains(got[0].Text, "🟢 <b>已恢复：2 个付款问题等人工处理</b>") || !strings.Contains(got[0].Markup, "/admin/monitor") {
		t.Fatalf("恢复时应通知：%+v", got)
	}
	resolved, err := m.Resolved(e.Ctx, 10)
	if err != nil || len(resolved) != 1 || resolved[0].ResolvedAt == 0 {
		t.Fatalf("恢复的告警进历史：%+v %v", resolved, err)
	}
	st, err := m.State(e.Ctx)
	if err != nil || st.LastRunAt == 0 || st.SentAt == 0 || st.SendError != "" {
		t.Fatalf("监控状态：%+v %v", st, err)
	}

	// 同一个问题再出现，是一条新告警。
	f.reset()
	addCase(t, e)
	c.Add(monitor.Every)
	evaluate(t, e, m)
	if as := openAlerts(t, e, m); len(as) != 1 || as[0].ID == resolved[0].ID {
		t.Fatalf("再出现应开新告警：%+v", as)
	}
}

// Telegram 发不出去时记下原因，告警当作还没通知，下一轮补发。
func TestSendFailureRetries(t *testing.T) {
	e, m, f, c := setup(t)
	configure(t, e, m, 42)
	f.mu.Lock()
	f.sendFail = true
	f.mu.Unlock()
	addCase(t, e)
	evaluate(t, e, m)
	st, err := m.State(e.Ctx)
	if err != nil || st.SendError == "" || st.SentAt != 0 {
		t.Fatalf("应记下发送失败：%+v %v", st, err)
	}
	if strings.Contains(st.SendError, token) {
		t.Fatalf("错误信息里不能有 Token：%s", st.SendError)
	}
	chats, err := m.Telegram.Chats(e.Ctx)
	if err != nil || len(chats) != 1 || !strings.Contains(chats[0].Error, "blocked") {
		t.Fatalf("会话应记下失败原因：%+v %v", chats, err)
	}
	if as := openAlerts(t, e, m); len(as) != 1 || as[0].NotifiedAt != 0 {
		t.Fatalf("没发出去的告警不算已通知：%+v", as)
	}

	f.mu.Lock()
	f.sendFail = false
	f.sent = nil
	f.mu.Unlock()
	c.Add(monitor.Every)
	evaluate(t, e, m)
	if got := f.messages(); len(got) != 1 || !strings.Contains(got[0].Text, "1 个付款问题等人工处理") {
		t.Fatalf("下一轮应补发：%+v", got)
	}
	if as := openAlerts(t, e, m); as[0].NotifiedAt == 0 {
		t.Fatalf("补发后算已通知：%+v", as)
	}
}

// 没配 Telegram 时照样记告警，只是不发；配好后把进行中的告警补发出去。
func TestAlertsWithoutTelegram(t *testing.T) {
	e, m, f, c := setup(t)
	addCase(t, e)
	evaluate(t, e, m)
	if as := openAlerts(t, e, m); len(as) != 1 || as[0].NotifiedAt != 0 {
		t.Fatalf("没配 Telegram 也要记告警：%+v", as)
	}
	configure(t, e, m, 42)
	c.Add(monitor.Every)
	evaluate(t, e, m)
	if got := f.messages(); len(got) != 1 {
		t.Fatalf("配好后补发：%+v", got)
	}
}

func TestConfigure(t *testing.T) {
	e, m, f, _ := setup(t)
	if _, err := m.Telegram.Configure(e.Ctx, "123456:wrong", monitor.Prefs{RemindHours: 1}); err == nil || !strings.Contains(err.Error(), "不认这个 Token") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("错的 Token：%v", err)
	}
	configure(t, e, m, 42)
	cfg, err := e.App.Settings.Telegram(e.Ctx)
	if err != nil || cfg.Token != token || cfg.BotUsername != "tillbox_test_bot" || cfg.RemindHours != 1 {
		t.Fatalf("保存的配置：%+v %v", cfg, err)
	}
	var raw store.Setting
	if err := e.DB().NewSelect().Model(&raw).Where("key = 'telegram'").Scan(e.Ctx); err != nil || strings.Contains(raw.Value, "test-token") {
		t.Fatalf("Token 要加密存库：%s %v", raw.Value, err)
	}
	if !cfg.Paid || !cfg.Claims {
		t.Fatalf("到账和申诉默认都发：%+v", cfg)
	}
	// 只改提醒间隔和开关，Token 不变。
	if _, err := m.Telegram.Configure(e.Ctx, "", monitor.Prefs{RemindHours: 0, Paid: false, Claims: true}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := e.App.Settings.Telegram(e.Ctx); cfg.Token != token || cfg.RemindHours != 0 || cfg.Paid || !cfg.Claims {
		t.Fatalf("留空不改 Token：%+v", cfg)
	}
	// 同一个机器人：绑定保留。
	configure(t, e, m)
	if chats, _ := m.Telegram.Chats(e.Ctx); len(chats) != 1 {
		t.Fatalf("同一个机器人不清绑定：%+v", chats)
	}
	// 换了机器人：原来的绑定收不到新机器人的消息，清掉。
	f.mu.Lock()
	f.botID = 999
	f.mu.Unlock()
	configure(t, e, m)
	if chats, _ := m.Telegram.Chats(e.Ctx); len(chats) != 0 {
		t.Fatalf("换机器人后应清掉绑定：%+v", chats)
	}
	if err := m.Telegram.Clear(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := e.App.Settings.Telegram(e.Ctx); cfg.Token != "" || cfg.BotUsername != "" || cfg != settings.DefaultTelegram() {
		t.Fatalf("清除后回到默认：%+v", cfg)
	}
}

// 旧版本存的设置里没有到账、申诉两项，读出来按默认都发。
func TestTelegramDefaultsForOldSettings(t *testing.T) {
	e := apptest.New(t)
	if err := e.App.Settings.SaveRaw(e.Ctx, "telegram", map[string]any{"bot_id": 1, "bot_username": "old_bot", "remind_hours": 3}); err != nil {
		t.Fatal(err)
	}
	cfg, err := e.App.Settings.Telegram(e.Ctx)
	if err != nil || !cfg.Paid || !cfg.Claims || cfg.RemindHours != 3 {
		t.Fatalf("旧设置：%+v %v", cfg, err)
	}
}

// paidOrder 下一单并按应付金额付款，返回到账后的订单。
func paidOrder(t *testing.T, e *apptest.Env, m *apptest.Merchant, out, name string, cents int64) *store.Order {
	t.Helper()
	o, _, err := e.App.Orders.Create(e.Ctx, order.CreateInput{
		MerchantID: m.ID, OutTradeNo: out, Version: order.V1, Channel: m.Channel, PayType: "alipay", Name: name,
		Money: epay.FormatCents(cents), Amount: cents, NotifyURL: e.Hook.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.Pay(o.TradeNo)
	o = e.Order(o.TradeNo)
	if o.Status != store.OrderPaid {
		t.Fatalf("订单应已支付：%+v", o)
	}
	return o
}

func deliver(t *testing.T, e *apptest.Env, m *monitor.Service) {
	t.Helper()
	if err := m.Deliver(e.Ctx); err != nil {
		t.Fatal(err)
	}
}

func eventStatus(t *testing.T, e *apptest.Env) map[string]int {
	t.Helper()
	var evs []store.AdminEvent
	if err := e.DB().NewSelect().Model(&evs).Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, ev := range evs {
		out[ev.Kind+":"+ev.Status]++
	}
	return out
}

// 收款到账、付款人申诉：和改动同一个事务排队，发一次；不带付款人信息；发送失败下一轮重发；关掉的不发。
func TestBusinessMessages(t *testing.T) {
	e, m, f, c := setup(t)
	mer := e.NewMerchant(nil)

	// 没配 Telegram 时到账的单不积压：记为跳过，配好后也不补发。
	paidOrder(t, e, mer, "before", "早到的单", 300)
	deliver(t, e, m)
	if got := eventStatus(t, e); got["paid:skipped"] != 1 || len(got) != 1 {
		t.Fatalf("没配 Telegram：%v", got)
	}
	configure(t, e, m, 42)
	deliver(t, e, m)
	if got := f.messages(); len(got) != 0 {
		t.Fatalf("配好后不补发跳过的：%+v", got)
	}

	o := paidOrder(t, e, mer, "A1", "账户充值", 100)
	deliver(t, e, m)
	got := f.messages()
	if len(got) != 1 || got[0].ChatID != "42" {
		t.Fatalf("到账发一条：%+v", got)
	}
	text := got[0].Text
	for _, want := range []string{"<b>收款到账 · ¥" + epay.FormatCents(o.PaidAmount), "商户　测试商户", "商品　账户充值", "订单　<code>" + o.TradeNo + "</code>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("到账消息缺 %q：\n%s", want, text)
		}
	}
	if !strings.Contains(got[0].Markup, `"text":"查看订单"`) || !strings.Contains(got[0].Markup, "/admin/orders?q="+o.TradeNo) {
		t.Fatalf("到账消息的按钮打开这笔订单：%s", got[0].Markup)
	}
	if strings.Contains(text, "测试付款人") || strings.Contains(text, "test***") {
		t.Fatalf("到账消息不能带付款人：\n%s", text)
	}
	if o.PaidAmount != o.Amount && !strings.Contains(text, "订单金额　¥1.00") {
		t.Fatalf("实付和订单金额不同时注明订单金额：\n%s", text)
	}
	deliver(t, e, m)
	if got := f.messages(); len(got) != 1 {
		t.Fatalf("发过的不再发：%+v", got)
	}

	// 付款人申诉：带类型、金额、订单号，不带联系方式和说明。
	f.reset()
	_, claim, err := e.App.Assistance.Submit(e.Ctx, o.ID, assistance.ClaimInput{
		Kind: "wrong_amount", PaymentRef: "2026101700000000000077", Amount: 250, PaidAt: store.Now() - 60_000,
		Contact: "PRIVATE-CONTACT", Message: "PRIVATE-MESSAGE",
	})
	if err != nil || claim == nil {
		t.Fatalf("提交申诉：%v", err)
	}
	deliver(t, e, m)
	got = f.messages()
	if len(got) != 1 {
		t.Fatalf("申诉发一条：%+v", got)
	}
	text = got[0].Text
	for _, want := range []string{"<b>付款申诉 · 金额付错</b>", "申报金额　¥2.50", "订单　<code>" + o.TradeNo + "</code>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("申诉消息缺 %q：\n%s", want, text)
		}
	}
	if !strings.Contains(got[0].Markup, "/admin/assistance") {
		t.Fatalf("申诉消息的按钮打开申诉页：%s", got[0].Markup)
	}
	if strings.Contains(text, "PRIVATE") {
		t.Fatalf("申诉消息不能带联系方式和说明：\n%s", text)
	}
	// 系统自己开的申诉（流水异常、通知失败）不发。
	if got := eventStatus(t, e); got["claim:sent"] != 1 || got["claim:pending"] != 0 {
		t.Fatalf("申诉消息状态：%v", got)
	}

	// 发送失败：留着，状态里记下错误，下一轮重发。
	f.reset()
	f.mu.Lock()
	f.sendFail = true
	f.mu.Unlock()
	paidOrder(t, e, mer, "A2", "会员", 200)
	paidOrder(t, e, mer, "A3", "会员", 500)
	c.Add(time.Second)
	deliver(t, e, m)
	if st, _ := m.State(e.Ctx); st.SendError == "" || st.SendErrorAt <= st.SentAt {
		t.Fatalf("发送失败要记下：%+v", st)
	}
	if got := eventStatus(t, e); got["paid:pending"] != 2 {
		t.Fatalf("发送失败的留着：%v", got)
	}
	f.reset()
	f.mu.Lock()
	f.sendFail = false
	f.mu.Unlock()
	c.Add(time.Second)
	deliver(t, e, m)
	got = f.messages()
	if len(got) != 1 || !strings.Contains(got[0].Text, "2 笔共 ¥") || !strings.Contains(got[0].Markup, "/admin/orders?state=paid") {
		t.Fatalf("重发时两笔合成一条：%+v", got)
	}
	if st, _ := m.State(e.Ctx); st.SentAt <= st.SendErrorAt {
		t.Fatalf("重发成功后状态：%+v", st)
	}

	// 关掉到账消息：之后的到账记为跳过，不发。
	f.reset()
	if _, err := m.Telegram.Configure(e.Ctx, "", monitor.Prefs{RemindHours: 1, Paid: false, Claims: true}); err != nil {
		t.Fatal(err)
	}
	paidOrder(t, e, mer, "A4", "会员", 100)
	deliver(t, e, m)
	if got := f.messages(); len(got) != 0 {
		t.Fatalf("关掉后不发：%+v", got)
	}
	if got := eventStatus(t, e); got["paid:skipped"] != 2 || got["paid:sent"] != 3 {
		t.Fatalf("关掉后记为跳过：%v", got)
	}
}

// 测试消息：每种用示例数据发一条，标“【测试】”，不动告警和待发队列；不认识的类型报错。
func TestTestMessages(t *testing.T) {
	e, m, f, _ := setup(t)
	configure(t, e, m, 42)
	if err := m.Test(e.Ctx, "nope"); !errors.Is(err, monitor.ErrTestKind) {
		t.Fatalf("不认识的类型：%v", err)
	}
	if err := m.Test(e.Ctx, "all"); err != nil {
		t.Fatal(err)
	}
	got := f.messages()
	if len(got) != len(monitor.TestKinds) {
		t.Fatalf("每种各发一条：%+v", got)
	}
	for i, want := range []string{"🔴 <b>【测试】示例渠道", "🟢 <b>【测试】已恢复：示例渠道", "🟢 <b>【测试】收款到账 · ¥12.00</b>", "📨 <b>【测试】付款申诉 · 已付款未到账</b>"} {
		if !strings.HasPrefix(got[i].Text, want) || got[i].ParseMode != "HTML" || got[i].Markup == "" {
			t.Errorf("第 %d 条应以 %q 开头：%+v", i+1, want, got[i])
		}
	}
	if n, _ := e.DB().NewSelect().Model((*store.Alert)(nil)).Count(e.Ctx); n != 0 {
		t.Fatalf("测试消息不开告警：%d", n)
	}
	if n, _ := e.DB().NewSelect().Model((*store.AdminEvent)(nil)).Count(e.Ctx); n != 0 {
		t.Fatalf("测试消息不进待发队列：%d", n)
	}
	if st, _ := m.State(e.Ctx); st.SentAt == 0 {
		t.Fatalf("发出后记下时间：%+v", st)
	}

	// 对外地址不是 https 时不做按钮，链接写进正文。
	f.reset()
	m.PublicURL = "http://127.0.0.1:8080"
	if err := m.Test(e.Ctx, "paid"); err != nil {
		t.Fatal(err)
	}
	if got := f.messages(); len(got) != 1 || got[0].Markup != "" || !strings.Contains(got[0].Text, "查看订单：http://127.0.0.1:8080/admin/orders") {
		t.Fatalf("http 地址写进正文：%+v", got)
	}
}

// Run 被到账唤醒后马上发，不等下一轮检查。
func TestWakeDeliversPaid(t *testing.T) {
	e, m, f, _ := setup(t)
	configure(t, e, m, 42)
	run(t, m)
	waitFor(t, "第一轮检查", func() bool {
		st, _ := m.State(e.Ctx)
		return st.LastRunAt > 0
	})
	o := paidOrder(t, e, e.NewMerchant(nil), "W1", "唤醒", 100)
	waitFor(t, "到账消息", func() bool {
		for _, s := range f.messages() {
			if strings.Contains(s.Text, o.TradeNo) {
				return true
			}
		}
		return false
	})
}

func run(t *testing.T, m *monitor.Service) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("等不到：%s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func update(id int, chatType, text string) string {
	b, _ := json.Marshal(map[string]any{
		"update_id": id,
		"message": map[string]any{
			"message_id": id, "date": 0, "text": text,
			"chat": map[string]any{"id": 42, "type": chatType, "first_name": "Ada", "username": "ada"},
		},
	})
	return string(b)
}

// 绑定：点链接后机器人收到“/start 验证码”，记下这个单聊；验证码不对的、群里的不认。
func TestBind(t *testing.T) {
	e, m, f, _ := setup(t)
	if _, err := m.Telegram.Bind(e.Ctx); !errors.Is(err, monitor.ErrNoToken) {
		t.Fatalf("没填 Token 时不能绑定：%v", err)
	}
	configure(t, e, m)
	run(t, m)
	var b monitor.Binding
	waitFor(t, "监控任务启动", func() bool {
		var err error
		b, err = m.Telegram.Bind(e.Ctx)
		return err == nil
	})
	code, ok := strings.CutPrefix(b.Link, "https://t.me/tillbox_test_bot?start=")
	if !ok || len(code) != 16 {
		t.Fatalf("绑定链接：%s", b.Link)
	}
	f.mu.Lock()
	f.updates = append(f.updates, "["+update(1, "private", "/start wrong")+","+update(2, "group", "/start "+code)+"]", "["+update(3, "private", "/start "+code)+"]")
	f.mu.Unlock()
	waitFor(t, "绑定完成", func() bool { return m.Telegram.Binding() == nil })
	chats, err := m.Telegram.Chats(e.Ctx)
	if err != nil || len(chats) != 1 || chats[0].ChatID != 42 || chats[0].Name != "Ada @ada" {
		t.Fatalf("绑定的会话：%+v %v", chats, err)
	}
	got := f.messages()
	if len(got) != 2 || !strings.Contains(got[0].Text, "验证码不对") || !strings.Contains(got[1].Text, "绑定成功") {
		t.Fatalf("机器人的回复：%+v", got)
	}
}

// 机器人设了 Webhook 时 getUpdates 收不到消息，后台要看到原因。
func TestBindConflict(t *testing.T) {
	e, m, f, _ := setup(t)
	configure(t, e, m)
	f.mu.Lock()
	f.conflict = true
	f.mu.Unlock()
	run(t, m)
	waitFor(t, "监控任务启动", func() bool {
		_, err := m.Telegram.Bind(e.Ctx)
		return err == nil
	})
	waitFor(t, "记下收消息的错误", func() bool {
		b := m.Telegram.Binding()
		return b != nil && strings.Contains(b.Error, "Webhook")
	})
	m.Telegram.CancelBind()
	if m.Telegram.Binding() != nil {
		t.Fatal("取消后不应还在绑定")
	}
}

// 查账从没成功过：从具备查账条件起超过停摆阈值才告警，成功查过一次就不再报。
func TestLedgerNeverSucceeded(t *testing.T) {
	e, m, _, c := setup(t)
	e.App.Pollers.SetNow(c.Now)
	pay, err := e.App.Settings.Payment(e.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	stale := func() []monitor.Problem {
		t.Helper()
		ps, err := m.Checker.Check(e.Ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []monitor.Problem
		for _, p := range ps {
			if strings.HasPrefix(p.Key, "ledger_stale:") {
				out = append(out, p)
			}
		}
		return out
	}

	if ps := stale(); len(ps) != 0 {
		t.Fatalf("还没开始查账时不该报停摆：%+v", ps)
	}
	e.Demo.FailWith = errors.New("网络不通")
	_ = e.App.Pollers.RunOnce(e.Ctx)
	if ps := stale(); len(ps) != 0 {
		t.Fatalf("刚开始查账就失败一次，不到阈值不该报停摆：%+v", ps)
	}
	c.Add(ledger.StaleAfter(pay) + time.Second)
	_ = e.App.Pollers.RunOnce(e.Ctx)
	ps := stale()
	if len(ps) != 1 || !strings.Contains(ps[0].Title, "还没有成功查过账") || !strings.Contains(ps[0].Detail, "网络不通") {
		t.Fatalf("就绪后一直没成功查账应告警：%+v", ps)
	}

	e.Demo.FailWith = nil
	if err := e.App.Pollers.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if ps := stale(); len(ps) != 0 {
		t.Fatalf("成功查过账后不该再报：%+v", ps)
	}
}
