package assistance_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/assistance"
	"tillbox/internal/channel"
	"tillbox/internal/order"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

type fixture struct {
	*apptest.Env
	model    *httptest.Server
	calls    atomic.Int64
	decision string
	seen     string
	onModel  func()
}

func newFixture(t *testing.T, decision string) *fixture {
	f := &fixture{Env: apptest.New(t), decision: decision}
	f.model = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		f.seen = string(b)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-model-key" {
			t.Error("unexpected model request")
		}
		if f.onModel != nil {
			f.onModel()
		}
		w.Header().Set("Content-Type", "application/json")
		if f.decision == "invalid" {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"not JSON"}}]}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": fmt.Sprintf(`{"action":%q,"reason":"按证据处理"}`, f.decision)}}}})
	}))
	t.Cleanup(f.model.Close)
	cfg := settings.Automation{Enabled: true, BaseURL: f.model.URL + "/v1", Model: "test-model", APIKey: "test-model-key", AutoLink: true, AutoNotify: true, AutoRecheck: true, IntervalSeconds: 30, MaxAttempts: 2}
	if err := f.App.Settings.SaveAutomation(f.Ctx, cfg); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) newOrder(cents int64) *store.Order {
	m := f.NewMerchant(nil)
	o, _, err := f.App.Orders.Create(f.Ctx, order.CreateInput{MerchantID: m.ID, OutTradeNo: "support-order", Version: order.V1, Channel: m.Channel, PayType: "alipay", Name: "support", Money: fmt.Sprintf("%d.%02d", cents/100, cents%100), Amount: cents, NotifyURL: f.Hook.URL})
	if err != nil {
		f.T.Fatal(err)
	}
	return o
}
func (f *fixture) late(o *store.Order) channel.Entry {
	_, err := f.App.DB.NewUpdate().Model((*store.Order)(nil)).Set("created_at = ?", store.Now()-10*time.Minute.Milliseconds()).Set("expires_at = ?", store.Now()-5*time.Minute.Milliseconds()).Where("id = ?", o.ID).Exec(f.Ctx)
	if err != nil {
		f.T.Fatal(err)
	}
	entry := channel.Entry{ExternalID: "verified-provider-transaction", AlipayOrderNo: "2026101600000000000001", Direction: store.DirIn, Amount: o.PayAmount, OccurredAt: time.Now().Add(-time.Minute), OtherAccount: "PRIVATE-BUYER", Raw: `{"private":"PRIVATE-RAW"}`}
	if _, err = f.App.Ledger.Ingest(f.Ctx, f.ChannelKey(), []channel.Entry{entry}); err != nil {
		f.T.Fatal(err)
	}
	return entry
}
func (f *fixture) submit(o *store.Order, e channel.Entry, kind string) (string, *store.AssistanceCase) {
	token, c, err := f.App.Assistance.Submit(f.Ctx, o.ID, assistance.ClaimInput{Kind: kind, PaymentRef: e.AlipayOrderNo, Amount: e.Amount, PaidAt: store.Ms(e.OccurredAt), Contact: "PRIVATE-CONTACT", Message: "请核对我的付款"})
	if err != nil {
		f.T.Fatal(err)
	}
	return token, c
}
func (f *fixture) tick() {
	if err := f.App.Settings.SaveRaw(f.Ctx, "automation_state", assistance.State{}); err != nil {
		f.T.Fatal(err)
	}
	if _, err := f.App.DB.NewUpdate().Model((*store.AssistanceCase)(nil)).Set("next_at = ?", 0).Where("status IN ('pending','waiting')").Exec(f.Ctx); err != nil {
		f.T.Fatal(err)
	}
	if err := f.App.Assistance.RunOnce(f.Ctx); err != nil && f.decision != "invalid" {
		f.T.Fatal(err)
	}
}
func (f *fixture) caseByID(id int64) store.AssistanceCase {
	var c store.AssistanceCase
	if err := f.App.DB.NewSelect().Model(&c).Where("id = ?", id).Scan(f.Ctx); err != nil {
		f.T.Fatal(err)
	}
	return c
}

func TestVerifiedLatePaymentAndPrivateReceipt(t *testing.T) {
	f := newFixture(t, "link_payment")
	o := f.newOrder(100)
	entry := f.late(o)
	token, c := f.submit(o, entry, "late")
	f.tick()
	if got := f.Order(o.TradeNo); got.Status != store.OrderPaid || got.PaidAmount != o.PayAmount {
		t.Fatalf("late payment not recovered: %s", got.Status)
	}
	if got := f.caseByID(c.ID); got.Status != "waiting" {
		t.Fatalf("case state %s", got.Status)
	}
	for _, secret := range []string{"PRIVATE-BUYER", "PRIVATE-CONTACT", "PRIVATE-RAW", entry.AlipayOrderNo, "test-model-key", token} {
		if strings.Contains(f.seen, secret) {
			t.Errorf("model facts leaked %s", secret)
		}
	}
	f.Deliver()
	calls := f.calls.Load()
	f.tick()
	// The merchant may answer success without crediting an order it expired.
	if got := f.caseByID(c.ID); got.Status != "manual" || !strings.Contains(got.Summary, "核对商户") {
		t.Fatalf("late recovery should ask for a merchant check after notify: %s %s", got.Status, got.Summary)
	}
	f.tick()
	checks, err := f.App.DB.NewSelect().Model((*store.AssistanceAction)(nil)).Where("case_id = ?", c.ID).Where("action = ?", "check_merchant").Count(f.Ctx)
	if err != nil || checks != 1 || f.caseByID(c.ID).Status != "manual" {
		t.Fatalf("merchant check repeated or closed by itself: %d %v", checks, err)
	}
	if f.calls.Load() != calls {
		t.Fatal("settled cases called model again")
	}
	if err := f.App.Assistance.UpdateCase(f.Ctx, c.ID, "resolve", "商户后台已入账"); err != nil || f.caseByID(c.ID).Status != "resolved" {
		t.Fatalf("admin could not close merchant check: %v", err)
	}
	if len(f.Hook.Got()) != 1 {
		t.Fatal("duplicate callback")
	}
	raw := f.Get("/web-api/admin/assistance")
	if raw.Status != 401 {
		t.Fatal("admin endpoint exposed")
	}
	progress := public(f.Env, "/web-api/self-service/progress", map[string]string{"receipt": token})
	if progress.Status != 200 || !strings.Contains(string(progress.Body), `"order_state":"paid"`) {
		t.Fatalf("receipt: %d %s", progress.Status, progress.Body)
	}
	for _, secret := range []string{"PRIVATE-CONTACT", entry.AlipayOrderNo, "请核对", `"token_hash"`, `"summary"`} {
		if strings.Contains(string(progress.Body), secret) {
			t.Fatal("private fields leaked to public progress")
		}
	}
	if r := public(f.Env, "/web-api/self-service/progress", map[string]string{"receipt": strings.Repeat("0", 64)}); r.Status != 404 {
		t.Fatal("forged receipt accepted")
	}
	var stored store.Setting
	if err := f.App.DB.NewSelect().Model(&stored).Where("key = ?", "automation").Scan(f.Ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.Value, "test-model-key") {
		t.Fatal("model key stored in plaintext")
	}
}

func TestModelCannotInventPaymentOrResolveDuplicate(t *testing.T) {
	for _, kind := range []string{"not_received", "duplicate", "wrong_amount"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, "link_payment")
			o := f.newOrder(200)
			e := channel.Entry{AlipayOrderNo: "2026101600000000000009", Amount: o.PayAmount, OccurredAt: time.Now()}
			_, c := f.submit(o, e, kind)
			f.tick()
			if got := f.Order(o.TradeNo); got.Status == store.OrderPaid {
				t.Fatal("model invented a payment")
			}
			if f.caseByID(c.ID).Status != "manual" {
				t.Fatal("unsupported action was not blocked")
			}
		})
	}
	t.Run("paid-duplicate-stays-manual", func(t *testing.T) {
		f := newFixture(t, "manual")
		o := f.newOrder(100)
		f.Pay(o.TradeNo)
		f.Deliver()
		_, c := f.submit(o, channel.Entry{AlipayOrderNo: "2026101600000000000022", Amount: o.PayAmount, OccurredAt: time.Now()}, "duplicate")
		f.tick()
		if f.caseByID(c.ID).Status != "manual" {
			t.Fatal("duplicate claim silently resolved because original order paid")
		}
	})
}

func TestVerifiedLinkRejectsChangedOrAmbiguousEvidence(t *testing.T) {
	for _, mode := range []string{"wrong-amount", "wrong-time", "closed", "disputed", "reused-amount", "conflict", "same-reference-other-order", "already-linked"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, "link_payment")
			o := f.newOrder(300)
			entry := f.late(o)
			_, c := f.submit(o, entry, "late")
			switch mode {
			case "wrong-amount":
				_, err := f.App.DB.NewUpdate().Model((*store.Transaction)(nil)).Set("amount = ?", o.PayAmount-1).Where("external_id = ?", entry.ExternalID).Exec(f.Ctx)
				if err != nil {
					t.Fatal(err)
				}
			case "wrong-time":
				_, err := f.App.DB.NewUpdate().Model(c).Set("paid_at = ?", store.Now()-time.Hour.Milliseconds()).WherePK().Exec(f.Ctx)
				if err != nil {
					t.Fatal(err)
				}
			case "closed", "disputed":
				state := store.OrderClosed
				if mode == "disputed" {
					state = store.OrderDisputed
				}
				_, err := f.App.DB.NewUpdate().Model((*store.Order)(nil)).Set("status = ?", state).Where("id = ?", o.ID).Exec(f.Ctx)
				if err != nil {
					t.Fatal(err)
				}
			case "reused-amount":
				other := f.newOrder(500)
				_, err := f.App.DB.NewUpdate().Model(other).Set("pay_amount = ?", o.PayAmount).Set("created_at = ?", store.Now()-2*time.Minute.Milliseconds()).WherePK().Exec(f.Ctx)
				if err != nil {
					t.Fatal(err)
				}
			case "conflict":
				var txn store.Transaction
				if err := f.App.DB.NewSelect().Model(&txn).Where("external_id = ?", entry.ExternalID).Scan(f.Ctx); err != nil {
					t.Fatal(err)
				}
				_, err := f.App.DB.NewInsert().Model(&store.Anomaly{Kind: store.AnomalyConflict, TransactionID: txn.ID, Status: store.AnomalyOpen, CreatedAt: store.Now()}).Exec(f.Ctx)
				if err != nil {
					t.Fatal(err)
				}
			case "same-reference-other-order":
				other := f.newOrder(500)
				f.submit(other, entry, "not_received")
			case "already-linked":
				other := f.newOrder(500)
				var txn store.Transaction
				if err := f.App.DB.NewSelect().Model(&txn).Where("external_id = ?", entry.ExternalID).Scan(f.Ctx); err != nil {
					t.Fatal(err)
				}
				if err := f.App.Ledger.Link(f.Ctx, txn.ID, other.ID, "test", "人工确认"); err != nil {
					t.Fatal(err)
				}
			}
			f.tick()
			if f.Order(o.TradeNo).Status == store.OrderPaid {
				t.Fatalf("unsafe link allowed: %s", mode)
			}
			if f.caseByID(c.ID).Status != "manual" {
				t.Fatalf("case not escalated: %s", mode)
			}
		})
	}
}

func TestNotificationRepairIsBoundedAndIdempotent(t *testing.T) {
	f := newFixture(t, "retry_notification")
	o := f.newOrder(100)
	f.Pay(o.TradeNo)
	f.Hook.Reply(500, "failure")
	f.Deliver()
	_, err := f.App.DB.NewUpdate().Model((*store.Notification)(nil)).Set("status = ?", store.NotifyDead).Set("last_at = ?", store.Now()-2*time.Minute.Milliseconds()).Where("order_id = ?", o.ID).Exec(f.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.Hook.Reply(200, "success")
	f.tick()
	var c store.AssistanceCase
	if err = f.App.DB.NewSelect().Model(&c).Where("source = ?", "notification").Scan(f.Ctx); err != nil {
		t.Fatal(err)
	}
	if c.Status != "resolved" || len(f.Hook.Got()) != 2 {
		t.Fatal("notification not recovered")
	}
	calls := f.calls.Load()
	f.tick()
	if len(f.Hook.Got()) != 2 || f.calls.Load() != calls {
		t.Fatal("duplicate repair")
	}
}

func TestInvalidModelOutputAndAttemptLimit(t *testing.T) {
	f := newFixture(t, "invalid")
	o := f.newOrder(100)
	_, c := f.submit(o, channel.Entry{AlipayOrderNo: "2026101600000000000099", Amount: o.PayAmount, OccurredAt: time.Now()}, "not_received")
	for i := 0; i < 4; i++ {
		f.tick()
	}
	got := f.caseByID(c.ID)
	if got.Status != "manual" || got.Attempts != 2 || f.calls.Load() != 2 {
		t.Fatalf("retry bound failed: %s %d calls=%d", got.Status, got.Attempts, f.calls.Load())
	}
	if f.Order(o.TradeNo).Status == store.OrderPaid {
		t.Fatal("invalid model output marked paid")
	}
}

func TestAdminOverrideDuringModelCallWins(t *testing.T) {
	f := newFixture(t, "link_payment")
	o := f.newOrder(100)
	entry := f.late(o)
	_, c := f.submit(o, entry, "late")
	f.onModel = func() {
		if err := f.App.Assistance.UpdateCase(f.Ctx, c.ID, "resolve", "人工接管，请联系商户"); err != nil {
			t.Error(err)
		}
	}
	f.tick()
	if f.Order(o.TradeNo).Status == store.OrderPaid {
		t.Fatal("stale model result executed after admin override")
	}
	if got := f.caseByID(c.ID); got.Status != "resolved" || got.PublicReply != "人工接管，请联系商户" {
		t.Fatal("admin override overwritten")
	}
}

func TestDisableDuringModelCallStopsActions(t *testing.T) {
	f := newFixture(t, "link_payment")
	o := f.newOrder(100)
	entry := f.late(o)
	_, c := f.submit(o, entry, "late")
	f.onModel = func() {
		cfg, err := f.App.Settings.Automation(f.Ctx)
		if err != nil {
			t.Error(err)
		}
		cfg.Enabled = false
		if err = f.App.Assistance.Configure(f.Ctx, cfg); err != nil {
			t.Error(err)
		}
	}
	f.tick()
	if f.Order(o.TradeNo).Status == store.OrderPaid {
		t.Fatal("action continued after disable")
	}
	if f.caseByID(c.ID).Status != "waiting" {
		t.Fatal("disabled case lost")
	}
}

func public(e *apptest.Env, path string, body any) *apptest.Response {
	e.T.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, e.URL+path, bytes.NewReader(b))
	if err != nil {
		e.T.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tillbox", "1")
	return e.Do(req)
}
func TestPublicSupportHTTPAndKeyPrivacy(t *testing.T) {
	f := newFixture(t, "manual")
	o := f.newOrder(100)
	payload := map[string]any{"trade_no": o.TradeNo, "kind": "not_received", "payment_ref": "2026101600000000000001", "amount": "1.01", "paid_at": store.Now(), "message": "付款遇到问题", "contact": "PRIVATE-CONTACT"}
	res := public(f.Env, "/web-api/self-service/claims", payload)
	if res.Status != 201 {
		t.Fatalf("submit: %d %s", res.Status, res.Body)
	}
	var created struct{ Receipt string }
	if err := json.Unmarshal(res.Body, &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Receipt) != 64 {
		t.Fatal("missing receipt")
	}
	if raw := public(f.Env, "/web-api/self-service/progress", map[string]string{"receipt": created.Receipt}); raw.Status != 200 {
		t.Fatalf("progress failed %d", raw.Status)
	}
	admin := f.Login()
	view := admin.Call("GET", "/web-api/admin/assistance", nil)
	if view.Status != 200 || strings.Contains(string(view.Body), "test-model-key") || strings.Contains(string(view.Body), created.Receipt) || strings.Contains(string(view.Body), `"token_hash"`) {
		t.Fatalf("unsafe admin config view %d", view.Status)
	}
	config, _ := f.App.Settings.Automation(f.Ctx)
	config.APIKey = ""
	bad := config
	bad.BaseURL = "https://different.example/v1"
	if res := admin.Call("PUT", "/web-api/admin/assistance/config", bad); res.Status != 400 {
		t.Fatal("key silently forwarded to changed endpoint")
	}
	if res := admin.Call("PUT", "/web-api/admin/assistance/config", config); res.Status != 200 {
		t.Fatalf("retain key: %s", res.Body)
	}
	saved, _ := f.App.Settings.Automation(f.Ctx)
	if saved.APIKey != "test-model-key" {
		t.Fatal("blank field dropped existing key")
	}
	if res := admin.Call("POST", "/web-api/admin/assistance/test", nil); res.Status != 200 {
		t.Fatalf("connection test: %d %s", res.Status, res.Body)
	}
	req, _ := http.NewRequest(http.MethodPost, f.URL+"/web-api/self-service/lookup", strings.NewReader(`{"trade_no":"`+o.TradeNo+`"}`))
	if f.Do(req).Status != 403 {
		t.Fatal("missing CSRF header accepted")
	}
	payload["message"] = strings.Repeat("长", 1100)
	if res := public(f.Env, "/web-api/self-service/claims", payload); res.Status != 400 {
		t.Fatal("overlong claim accepted")
	}
	payload["message"] = "合法说明"
	for i := 0; i < 12; i++ {
		res = public(f.Env, "/web-api/self-service/claims", payload)
	}
	if res.Status != 429 {
		t.Fatal("public claims not rate limited")
	}
}

func TestFindOrderFromVerifiedClaim(t *testing.T) {
	for _, mode := range []string{"late", "paid"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, "link_payment")
			o := f.newOrder(100)
			var entry channel.Entry
			if mode == "late" {
				entry = f.late(o)
			} else {
				f.Pay(o.TradeNo)
				f.Deliver()
				var txn store.Transaction
				if err := f.App.DB.NewSelect().Model(&txn).Where("order_id = ?", o.ID).Scan(f.Ctx); err != nil {
					t.Fatal(err)
				}
				entry = channel.Entry{AlipayOrderNo: txn.AlipayOrderNo, ExternalID: txn.ExternalID, Amount: txn.Amount, OccurredAt: time.UnixMilli(txn.OccurredAt)}
				if entry.AlipayOrderNo == "" {
					entry.AlipayOrderNo = entry.ExternalID
				}
			}
			token, c, err := f.App.Assistance.Submit(f.Ctx, 0, assistance.ClaimInput{Kind: "not_received", PaymentRef: entry.AlipayOrderNo, Amount: entry.Amount, PaidAt: store.Ms(entry.OccurredAt)})
			if err != nil {
				t.Fatal(err)
			}
			if c.OrderID != o.ID || c.TransactionID == 0 {
				t.Fatal("verified claim not associated")
			}
			if mode == "late" && f.Order(o.TradeNo).Status == store.OrderPaid {
				t.Fatal("finding an order changed financial state")
			}
			progress := public(f.Env, "/web-api/self-service/progress", map[string]string{"receipt": token})
			if progress.Status != 200 || !strings.Contains(string(progress.Body), o.TradeNo) {
				t.Fatal("verified order unavailable via receipt")
			}
			f.tick()
			f.Deliver()
			f.tick()
			// A late recovery waits for an administrator to confirm the merchant credited it.
			want := "resolved"
			if mode == "late" {
				want = "manual"
			}
			if f.Order(o.TradeNo).Status != store.OrderPaid || f.caseByID(c.ID).Status != want {
				t.Fatal("found claim did not complete normal verified workflow")
			}
			if len(f.Hook.Got()) != 1 {
				t.Fatal("duplicate notification")
			}
		})
	}
}

func TestFindOrderRejectsUnprovenOrAmbiguousClaims(t *testing.T) {
	for _, mode := range []string{"missing-reference", "wrong-amount", "wrong-time", "outbound", "other-channel", "duplicate-reference", "amount-reuse", "blocked-anomaly", "closed", "conflicting-claim"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, "link_payment")
			o := f.newOrder(100)
			e := f.late(o)
			var txn store.Transaction
			if err := f.App.DB.NewSelect().Model(&txn).Where("external_id = ?", e.ExternalID).Scan(f.Ctx); err != nil {
				t.Fatal(err)
			}
			input := assistance.ClaimInput{Kind: "late", PaymentRef: e.AlipayOrderNo, Amount: e.Amount, PaidAt: store.Ms(e.OccurredAt)}
			var err error
			switch mode {
			case "missing-reference":
				input.PaymentRef = "unknown-reference"
			case "wrong-amount":
				input.Amount++
			case "wrong-time":
				input.PaidAt -= time.Hour.Milliseconds()
			case "outbound":
				_, err = f.App.DB.NewUpdate().Model(&txn).Set("direction = ?", store.DirOut).WherePK().Exec(f.Ctx)
			case "other-channel":
				_, err = f.App.DB.NewUpdate().Model(&txn).Set("channel = ?", "another-channel").WherePK().Exec(f.Ctx)
			case "duplicate-reference":
				txn.ID = 0
				txn.ExternalID = "another-provider-reference"
				_, err = f.App.DB.NewInsert().Model(&txn).Exec(f.Ctx)
			case "amount-reuse":
				other := f.newOrder(500)
				_, err = f.App.DB.NewUpdate().Model(other).Set("pay_amount = ?", o.PayAmount).Set("created_at = ?", store.Now()-2*time.Minute.Milliseconds()).WherePK().Exec(f.Ctx)
			case "blocked-anomaly":
				_, err = f.App.DB.NewInsert().Model(&store.Anomaly{Kind: store.AnomalyUnlinked, TransactionID: txn.ID, Status: store.AnomalyResolved, CreatedAt: store.Now()}).Exec(f.Ctx)
			case "closed":
				_, err = f.App.DB.NewUpdate().Model(o).Set("status = ?", store.OrderClosed).WherePK().Exec(f.Ctx)
			case "conflicting-claim":
				other := f.newOrder(500)
				f.submit(other, e, "late")
			}
			if err != nil {
				t.Fatal(err)
			}
			token, c, err := f.App.Assistance.Submit(f.Ctx, 0, input)
			if err != nil {
				t.Fatal(err)
			}
			if c.OrderID != 0 || c.Status != "manual" {
				t.Fatal("unproven claim disclosed an order")
			}
			progress := public(f.Env, "/web-api/self-service/progress", map[string]string{"receipt": token})
			if progress.Status != 200 || !strings.Contains(string(progress.Body), `"trade_no":""`) || strings.Contains(string(progress.Body), o.TradeNo) {
				t.Fatal("unbound progress leaked candidate")
			}
			if f.Order(o.TradeNo).Status == store.OrderPaid {
				t.Fatal("claim changed financial state")
			}
		})
	}
}

func TestUnboundPublicClaimAndAdminAssociation(t *testing.T) {
	f := newFixture(t, "manual")
	o := f.newOrder(100)
	payload := map[string]any{"trade_no": "", "kind": "not_received", "payment_ref": "unmatched-reference", "amount": "1.01", "paid_at": store.Now(), "message": "PRIVATE-MESSAGE", "contact": "PRIVATE-CONTACT"}
	response := public(f.Env, "/web-api/self-service/claims", payload)
	if response.Status != 201 {
		t.Fatalf("missing order rejected: %d", response.Status)
	}
	var created struct{ Receipt string }
	if err := json.Unmarshal(response.Body, &created); err != nil {
		t.Fatal(err)
	}
	var c store.AssistanceCase
	if err := f.App.DB.NewSelect().Model(&c).Where("token_hash = ?", assistance.TokenHash(created.Receipt)).Scan(f.Ctx); err != nil {
		t.Fatal(err)
	}
	if c.OrderID != 0 || c.Status != "manual" {
		t.Fatal("unmatched claim not retained for manual review")
	}
	progress := public(f.Env, "/web-api/self-service/progress", map[string]string{"receipt": created.Receipt})
	if progress.Status != 200 || strings.Contains(string(progress.Body), "PRIVATE-") {
		t.Fatal("unbound progress failed or leaked private data")
	}
	admin := f.Login()
	path := fmt.Sprintf("/web-api/admin/assistance/cases/%d", c.ID)
	if res := admin.Call("POST", path, map[string]string{"action": "associate", "trade_no": o.TradeNo, "note": ""}); res.Status != 400 {
		t.Fatal("association without evidence note accepted")
	}
	if res := admin.Call("POST", path, map[string]string{"action": "associate", "trade_no": "not-an-order", "note": "核实原业务站和付款凭证"}); res.Status != 400 {
		t.Fatal("invalid order accepted")
	}
	if res := admin.Call("POST", path, map[string]string{"action": "associate", "trade_no": o.TradeNo, "note": "核实原业务站和付款凭证"}); res.Status != 200 {
		t.Fatalf("admin association: %d", res.Status)
	}
	if f.Order(o.TradeNo).Status == store.OrderPaid || f.caseByID(c.ID).OrderID != o.ID {
		t.Fatal("association did not preserve financial state")
	}
	if res := admin.Call("POST", path, map[string]string{"action": "associate", "trade_no": o.TradeNo, "note": "再次关联"}); res.Status != 400 {
		t.Fatal("already associated case changed")
	}
	progress = public(f.Env, "/web-api/self-service/progress", map[string]string{"receipt": created.Receipt})
	if progress.Status != 200 || !strings.Contains(string(progress.Body), o.TradeNo) {
		t.Fatal("manual association not available to correct receipt")
	}
	payload["trade_no"] = "bad-number"
	if res := public(f.Env, "/web-api/self-service/claims", payload); res.Status != 404 {
		t.Fatal("invalid nonempty number treated as unknown")
	}
}

func TestUnboundClaimsHavePerReferenceQuota(t *testing.T) {
	f := newFixture(t, "manual")
	input := assistance.ClaimInput{Kind: "not_received", PaymentRef: "reference-one", Amount: 101, PaidAt: store.Now()}
	for i := 0; i < 5; i++ {
		if _, _, err := f.App.Assistance.Submit(f.Ctx, 0, input); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.App.Assistance.Submit(f.Ctx, 0, input); !errors.Is(err, assistance.ErrRateLimit) {
		t.Fatal("reference quota bypassed")
	}
	input.PaymentRef = "reference-two"
	if _, _, err := f.App.Assistance.Submit(f.Ctx, 0, input); err != nil {
		t.Fatal("one unknown claim blocked another payer")
	}
}

// ledgerCase 收一笔对不上订单的流水，返回它的异常和对应的处理记录。
func (f *fixture) ledgerCase() (store.Anomaly, store.AssistanceCase) {
	f.newOrder(100)
	entry := channel.Entry{ExternalID: "unmatched-provider-transaction", Direction: store.DirIn, Amount: 777, OccurredAt: time.Now()}
	if _, err := f.App.Ledger.Ingest(f.Ctx, f.ChannelKey(), []channel.Entry{entry}); err != nil {
		f.T.Fatal(err)
	}
	f.tick()
	var a store.Anomaly
	if err := f.App.DB.NewSelect().Model(&a).Where("status = ?", store.AnomalyOpen).Scan(f.Ctx); err != nil {
		f.T.Fatal(err)
	}
	var c store.AssistanceCase
	if err := f.App.DB.NewSelect().Model(&c).Where("anomaly_id = ?", a.ID).Scan(f.Ctx); err != nil {
		f.T.Fatal(err)
	}
	return a, c
}

func TestManualOnlyCasesSkipModel(t *testing.T) {
	f := newFixture(t, "manual")
	_, c := f.ledgerCase()
	if c.Status != "manual" || f.calls.Load() != 0 {
		t.Fatalf("账务异常应直接转人工且不调模型：%s calls=%d", c.Status, f.calls.Load())
	}
}

func TestCasesFollowLedgerAndNotificationState(t *testing.T) {
	t.Run("anomaly-resolved-on-ledger-page", func(t *testing.T) {
		f := newFixture(t, "manual")
		a, c := f.ledgerCase()
		if err := f.App.Ledger.Resolve(f.Ctx, a.ID, "test", "已线下退款"); err != nil {
			t.Fatal(err)
		}
		cfg, _ := f.App.Settings.Automation(f.Ctx)
		cfg.Enabled = false
		if err := f.App.Settings.SaveAutomation(f.Ctx, cfg); err != nil {
			t.Fatal(err)
		}
		f.tick()
		if got := f.caseByID(c.ID); got.Status != "resolved" {
			t.Fatalf("异常已处理但记录仍是 %s", got.Status)
		}
	})
	t.Run("case-resolved-on-assistance-page", func(t *testing.T) {
		f := newFixture(t, "manual")
		a, c := f.ledgerCase()
		if err := f.App.Assistance.UpdateCase(f.Ctx, c.ID, "resolve", "已线下退款"); err != nil {
			t.Fatal(err)
		}
		var got store.Anomaly
		if err := f.App.DB.NewSelect().Model(&got).Where("id = ?", a.ID).Scan(f.Ctx); err != nil {
			t.Fatal(err)
		}
		if got.Status != store.AnomalyResolved || got.Note != "已线下退款" {
			t.Fatalf("处理记录已结案但异常仍是 %s", got.Status)
		}
	})
	t.Run("notification-resent-elsewhere", func(t *testing.T) {
		f := newFixture(t, "manual")
		o := f.newOrder(100)
		f.Pay(o.TradeNo)
		f.Hook.Reply(500, "failure")
		f.Deliver()
		_, err := f.App.DB.NewUpdate().Model((*store.Notification)(nil)).Set("status = ?", store.NotifyDead).Set("last_at = ?", store.Now()-2*time.Minute.Milliseconds()).Where("order_id = ?", o.ID).Exec(f.Ctx)
		if err != nil {
			t.Fatal(err)
		}
		f.tick()
		var c store.AssistanceCase
		if err = f.App.DB.NewSelect().Model(&c).Where("source = ?", "notification").Scan(f.Ctx); err != nil {
			t.Fatal(err)
		}
		if c.Status != "manual" {
			t.Fatalf("模型选了人工，记录应是 manual：%s", c.Status)
		}
		f.Hook.Reply(200, "success")
		if _, err = f.App.Notifier.Resend(f.Ctx, o.ID); err != nil {
			t.Fatal(err)
		}
		f.tick()
		if got := f.caseByID(c.ID); got.Status != "resolved" {
			t.Fatalf("通知已送达但记录仍是 %s", got.Status)
		}
	})
}

func TestOrderQuotaCannotBlockProvenClaim(t *testing.T) {
	f := newFixture(t, "manual")
	o := f.newOrder(100)
	entry := f.late(o)
	for i := 0; i < 10; i++ {
		ref := fmt.Sprintf("forged-reference-%02d", i)
		if _, _, err := f.App.Assistance.Submit(f.Ctx, o.ID, assistance.ClaimInput{Kind: "not_received", PaymentRef: ref, Amount: o.PayAmount, PaidAt: store.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.App.Assistance.Submit(f.Ctx, o.ID, assistance.ClaimInput{Kind: "not_received", PaymentRef: "forged-reference-99", Amount: o.PayAmount, PaidAt: store.Now()}); !errors.Is(err, assistance.ErrRateLimit) {
		t.Fatal("订单额度没有生效")
	}
	if _, _, err := f.App.Assistance.Submit(f.Ctx, o.ID, assistance.ClaimInput{Kind: "late", PaymentRef: entry.AlipayOrderNo, Amount: entry.Amount, PaidAt: store.Ms(entry.OccurredAt)}); err != nil {
		t.Fatalf("真实付款人的申诉被他人耗光的额度挡住：%v", err)
	}
}
