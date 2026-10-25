package admin

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/order"
	"tillbox/internal/payroute"
	"tillbox/internal/store"
)

// Test orders use the normal amount pool and ledger, but only notify our own
// signature-checking receiver. No business account can be credited by this page.
var testMoneyPattern = regexp.MustCompile(`^\d+(\.\d{1,2})?$`)

func (a *API) createPaymentTest(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		MerchantID int64  `json:"merchant_id"`
		Money      string `json:"money"`
		Version    int    `json:"version"`
		PayType    string `json:"pay_type"` // 空：商户只开了一种方式时用它，开了几种时在收银台选
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	amount, valid := epay.ParseMoney(in.Money)
	if !testMoneyPattern.MatchString(in.Money) || !valid || amount < 1 || amount > 10000 {
		return badRequest("测试金额须为 0.01–100.00 元，最多两位小数")
	}
	if in.Version != order.V1 && in.Version != order.V2 {
		return badRequest("请选择 V1 或 V2")
	}
	m, err := a.Merchants.Get(r.Context(), in.MerchantID)
	if err != nil || !m.Enabled {
		return badRequest("请选择已启用的商户")
	}
	methods := payroute.Methods(r.Context(), a.Channels, m.Routes)
	if len(methods) == 0 {
		return badRequest("这个商户还没有开放任何支付方式")
	}
	typ := in.PayType
	if typ == "" && len(methods) == 1 {
		typ = methods[0].Type
	}
	var pick *payroute.Method
	if typ != "" {
		mt, found := payroute.Find(methods, typ)
		if !found {
			return badRequest("这个商户没有开放" + channel.TypeLabel(typ))
		}
		if !mt.Ready() {
			return badRequest(channel.TypeLabel(typ) + "现在收不了款：" + mt.Problem)
		}
		pick = &mt
	} else if !slices.ContainsFunc(methods, payroute.Method.Ready) {
		return badRequest("这个商户开放的支付方式现在都收不了款")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	create := order.CreateInput{
		IsTest: true, MerchantID: m.ID, OutTradeNo: "test-" + hex.EncodeToString(nonce[:]),
		Version: in.Version, NoTail: true, Name: "后台支付测试", Money: epay.FormatCents(amount), Amount: amount,
		NotifyURL: a.BaseURL(r) + "/web-api/payment-test/notify", ClientIP: a.ClientIP(r), Device: "pc",
	}
	if pick != nil {
		create.Channel, create.NoTail, create.PayType = pick.Row.Key(), pick.Confirming(), pick.Type
	}
	o, _, err := a.Orders.Create(r.Context(), create)
	if err != nil {
		return badRequest(err.Error())
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "payment_test.create", o.TradeNo, "")
	if pick != nil {
		if _, err := payroute.Choose(r.Context(), a.Orders, a.Confirm, o, *pick, a.BaseURL(r)); err != nil {
			return badRequest("向第三方平台预下单失败：" + err.Error())
		}
		if !pick.Confirming() {
			a.Pollers.Kick(o.Channel)
		}
	}
	ok(w, map[string]any{"order": o, "cashier_url": a.BaseURL(r) + "/cashier/" + o.TradeNo})
	return nil
}

func (a *API) testReceipt(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var receipt store.TestReceipt
	// An absent receipt is a normal state while waiting for payment.
	err = a.DB.NewSelect().Model(&receipt).Where("order_id = ?", id).Scan(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	ok(w, map[string]any{"received": err == nil, "receipt": receipt})
	return nil
}

// This public endpoint acknowledges only an already-paid test order with the
// exact signed notification fields. It never marks an order paid.
func (a *API) receivePaymentTest(w http.ResponseWriter, r *http.Request) {
	fail := func() { http.Error(w, "fail", http.StatusBadRequest) }
	p := epay.Params{}
	for k, vs := range r.URL.Query() {
		if len(vs) != 1 {
			fail()
			return
		}
		p[k] = vs[0]
	}
	o, err := a.Orders.ByTradeNo(r.Context(), p["trade_no"])
	if err != nil || !o.IsTest || o.Status != store.OrderPaid {
		fail()
		return
	}
	now := time.Now()
	if o.Version == order.V2 {
		ts, err := strconv.ParseInt(p["timestamp"], 10, 64)
		if err != nil || ts < now.Unix()-300 || ts > now.Unix()+300 {
			fail()
			return
		}
		now = time.Unix(ts, 0)
	}
	expected, err := a.Signer.Params(r.Context(), o, now)
	if err != nil || epay.SignContent(expected) != epay.SignContent(p) || p["sign_type"] != expected["sign_type"] {
		fail()
		return
	}
	valid := false
	if o.Version == order.V1 {
		m, err := a.Merchants.Get(r.Context(), o.MerchantID)
		valid = err == nil && epay.MD5Verify(p, m.Key)
	} else {
		platform, err := a.Settings.Platform(r.Context())
		if err == nil {
			pub, err := epay.ParsePublicKey(platform.PublicKey)
			valid = err == nil && epay.RSAVerify(epay.SignContent(p), p["sign"], pub)
		}
	}
	if !valid {
		fail()
		return
	}
	t := store.Now()
	receipt := &store.TestReceipt{OrderID: o.ID, Deliveries: 1, FirstAt: t, LastAt: t}
	_, err = a.DB.NewInsert().Model(receipt).On("CONFLICT (order_id) DO UPDATE").
		Set("deliveries = receipt.deliveries + 1").Set("last_at = EXCLUDED.last_at").Exec(r.Context())
	if err != nil {
		slog.Error("记录测试回调失败", "err", err)
		http.Error(w, "fail", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("success"))
}
