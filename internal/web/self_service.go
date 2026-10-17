package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"tillbox/internal/assistance"
	"tillbox/internal/epay"
	"tillbox/internal/order"
	"tillbox/internal/store"
)

var ticketPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func registerSelfService(mux *http.ServeMux, d Deps) {
	limiter := &assistance.Limiter{}
	serve := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.Header.Get("X-Tillbox") == "" {
				jsonOut(w, 403, map[string]string{"error": "请通过自助页面提交"})
				return
			}
			max := 90
			if r.URL.Path == "/web-api/self-service/claims" {
				max = 8
			}
			if !limiter.Allow(r.URL.Path+":"+d.Admin.ClientIP(r), max) {
				jsonOut(w, 429, map[string]string{"error": "请求过于频繁，请稍后再试"})
				return
			}
			h(w, r)
		}
	}
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if dec.Decode(v) != nil {
			jsonOut(w, 400, map[string]string{"error": "提交内容格式不正确"})
			return false
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			jsonOut(w, 400, map[string]string{"error": "提交内容包含额外数据"})
			return false
		}
		return true
	}
	load := func(w http.ResponseWriter, r *http.Request, no string) *store.Order {
		if !tradeNoRe.MatchString(no) {
			jsonOut(w, 404, map[string]string{"error": "订单不存在，请核对平台订单号"})
			return nil
		}
		o, err := d.Orders.ByTradeNo(r.Context(), no)
		if err != nil {
			jsonOut(w, 404, map[string]string{"error": "订单不存在，请核对平台订单号"})
			return nil
		}
		return o
	}
	mux.HandleFunc("POST /web-api/self-service/lookup", serve(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TradeNo string `json:"trade_no"`
		}
		if !decode(w, r, &in) {
			return
		}
		o := load(w, r, strings.TrimSpace(in.TradeNo))
		if o == nil {
			return
		}
		jsonOut(w, 200, map[string]any{"trade_no": o.TradeNo, "state": order.Effective(o, time.Now()), "pay_amount": epay.FormatCents(o.PayAmount), "paid_at": o.PaidAt})
	}))
	mux.HandleFunc("POST /web-api/self-service/claims", serve(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TradeNo    string `json:"trade_no"`
			Amount     string `json:"amount"`
			Kind       string `json:"kind"`
			PaymentRef string `json:"payment_ref"`
			PaidAt     int64  `json:"paid_at"`
			Message    string `json:"message"`
			Contact    string `json:"contact"`
		}
		if !decode(w, r, &in) {
			return
		}
		var orderID int64
		if no := strings.TrimSpace(in.TradeNo); no != "" {
			o := load(w, r, no)
			if o == nil {
				return
			}
			orderID = o.ID
		}
		amount, valid := epay.ParseMoney(in.Amount)
		if !valid {
			jsonOut(w, 400, map[string]string{"error": "实付金额不正确"})
			return
		}
		token, c, err := d.Assistance.Submit(r.Context(), orderID, assistance.ClaimInput{Kind: in.Kind, PaymentRef: in.PaymentRef, Amount: amount, PaidAt: in.PaidAt, Message: in.Message, Contact: in.Contact})
		if err != nil {
			status := 400
			msg := assistance.ErrInvalidClaim.Error()
			if errors.Is(err, assistance.ErrRateLimit) {
				status = 429
				msg = err.Error()
			} else if !errors.Is(err, assistance.ErrInvalidClaim) {
				status = 500
				msg = "暂时无法提交，请稍后重试"
			}
			jsonOut(w, status, map[string]string{"error": msg})
			return
		}
		jsonOut(w, 201, map[string]any{"receipt": token, "status": c.Status, "reply": c.PublicReply})
	}))
	mux.HandleFunc("POST /web-api/self-service/progress", serve(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Receipt string `json:"receipt"`
		}
		if !decode(w, r, &in) {
			return
		}
		if !ticketPattern.MatchString(in.Receipt) {
			jsonOut(w, 404, map[string]string{"error": "查询凭证无效"})
			return
		}
		var c store.AssistanceCase
		if err := d.Assistance.DB.NewSelect().Model(&c).Where("token_hash = ?", assistance.TokenHash(in.Receipt)).Scan(r.Context()); err != nil {
			jsonOut(w, 404, map[string]string{"error": "查询凭证无效"})
			return
		}
		if c.OrderID == 0 {
			jsonOut(w, 200, map[string]any{"status": c.Status, "reply": c.PublicReply, "updated_at": c.UpdatedAt, "order_state": "", "trade_no": ""})
			return
		}
		o, err := d.Orders.ByID(r.Context(), c.OrderID)
		if err != nil {
			jsonOut(w, 500, map[string]string{"error": "订单状态暂不可用"})
			return
		}
		jsonOut(w, 200, map[string]any{"status": c.Status, "reply": c.PublicReply, "updated_at": c.UpdatedAt, "order_state": order.Effective(o, time.Now()), "trade_no": o.TradeNo})
	}))
}
