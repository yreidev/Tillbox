package admin_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"tillbox/internal/apptest"
	ordersvc "tillbox/internal/order"
	"tillbox/internal/store"
)

func TestPaymentTesting(t *testing.T) {
	for _, version := range []int{ordersvc.V1, ordersvc.V2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			e := apptest.New(t)
			m := e.NewMerchant(nil)
			input := map[string]any{"merchant_id": m.ID, "money": "1.00", "version": version, "notify_url": e.Hook.URL}
			expect(t, raw(e, "POST", "/web-api/admin/payment-tests", `{}`, map[string]string{"X-Tillbox": "1"}), http.StatusUnauthorized, "")
			a := e.Login()
			r := a.Call("POST", "/web-api/admin/payment-tests", input)
			expect(t, r, http.StatusOK, "")
			res := decode[struct {
				Order store.Order `json:"order"`
			}](t, r)
			o := res.Order
			if !o.IsTest || o.NotifyURL != e.URL+"/web-api/payment-test/notify" || o.PayAmount != 101 {
				t.Fatalf("invalid test order: %+v", o)
			}
			expect(t, e.Get("/web-api/payment-test/notify?trade_no="+o.TradeNo), http.StatusBadRequest, "fail")
			expect(t, a.Call("GET", fmt.Sprintf("/web-api/admin/payment-tests/%d/receipt", o.ID), nil), 200, `"received":false`)
			e.Pay(o.TradeNo)
			e.Deliver()
			var n store.Notification
			if err := e.DB().NewSelect().Model(&n).Where("order_id = ?", o.ID).Scan(e.Ctx); err != nil {
				t.Fatal(err)
			}
			if n.Status != store.NotifySuccess {
				t.Fatalf("test notification failed: %+v", n)
			}
			if _, err := e.App.Notifier.Resend(e.Ctx, o.ID); err != nil {
				t.Fatal(err)
			}
			var receipt store.TestReceipt
			if err := e.DB().NewSelect().Model(&receipt).Where("order_id = ?", o.ID).Scan(e.Ctx); err != nil {
				t.Fatal(err)
			}
			if receipt.Deliveries != 2 || len(e.Hook.Got()) != 0 {
				t.Fatal("test callback escaped or replay lost")
			}
			var attempt store.NotifyAttempt
			if err := e.DB().NewSelect().Model(&attempt).Where("order_id = ?", o.ID).Order("id DESC").Limit(1).Scan(e.Ctx); err != nil {
				t.Fatal(err)
			}
			// Changing any signed field must fail without acknowledging delivery.
			req, _ := http.NewRequest(http.MethodGet, attempt.URL, nil)
			q := req.URL.Query()
			q.Set("money", "2.00")
			req.URL.RawQuery = q.Encode()
			expect(t, e.Do(req), http.StatusBadRequest, "fail")
			overview := decode[typeofOverview](t, a.Call("GET", "/web-api/admin/overview", nil))
			if overview.Today.Count != 0 || overview.Today.Amount != 0 {
				t.Fatal("test payment polluted sales")
			}
			stats, err := e.App.Orders.MerchantStats(e.Ctx, m.ID)
			if err != nil || stats.Total != 0 {
				t.Fatalf("merchant stats include test: %+v %v", stats, err)
			}
			if _, err := e.App.Orders.Find(e.Ctx, m.ID, o.TradeNo, ""); !errors.Is(err, ordersvc.ErrNotFound) {
				t.Fatalf("merchant API exposes test: %v", err)
			}
			expect(t, a.Call("GET", "/web-api/admin/orders?is_test=true", nil), 200, o.TradeNo)
			for _, money := range []string{"0", "-1", "100.01", "1.001", "oops"} {
				input["money"] = money
				expect(t, a.Call("POST", "/web-api/admin/payment-tests", input), 400, "")
			}
			if !hasAudit(t, e, "payment_test.create") {
				t.Fatal("missing audit")
			}
		})
	}
}

type typeofOverview struct {
	Today struct {
		Count  int64 `json:"count"`
		Amount int64 `json:"amount"`
	} `json:"today"`
}
