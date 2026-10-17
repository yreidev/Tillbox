package web

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/channel/fake"
	"tillbox/internal/channel/registry"
	"tillbox/internal/confirm"
	"tillbox/internal/epay"
	"tillbox/internal/ledger"
	"tillbox/internal/notify"
	"tillbox/internal/order"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// ReturnWindow 是付款后还能跳回商户的时长（和官方一样 5 分钟）。
const ReturnWindow = 5 * time.Minute

var tradeNoRe = regexp.MustCompile(`^[0-9]{19}$`)

type cashier struct {
	orders   *order.Service
	settings *settings.Service
	channels *registry.Registry
	pollers  *ledger.Pollers
	confirm  *confirm.Service
	signer   *notify.Signer
	baseURL  func(r *http.Request) string
}

func (c *cashier) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /web-api/cashier/{trade_no}", c.get)
	mux.HandleFunc("POST /web-api/cashier/{trade_no}/paid", c.paid)
	mux.HandleFunc("POST /web-api/cashier/{trade_no}/pay", c.pay)
	mux.HandleFunc("POST /web-api/cashier/{trade_no}/go", c.goPay)
	mux.HandleFunc("GET /web-api/cashier/{trade_no}/return", c.returnURL)
}

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (c *cashier) load(w http.ResponseWriter, r *http.Request) *store.Order {
	no := r.PathValue("trade_no")
	if !tradeNoRe.MatchString(no) {
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "订单不存在"})
		return nil
	}
	o, err := c.orders.ByTradeNo(r.Context(), no)
	if errors.Is(err, order.ErrNotFound) {
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "订单不存在"})
		return nil
	}
	if err != nil {
		slog.Error("收银台读取订单失败", "trade_no", no, "err", err)
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "系统繁忙，请稍后刷新"})
		return nil
	}
	return o
}

func (c *cashier) get(w http.ResponseWriter, r *http.Request) {
	o := c.load(w, r)
	if o == nil {
		return
	}
	now := time.Now()
	state := order.Effective(o, now)
	site, _ := c.settings.Site(r.Context())
	res := map[string]any{
		"trade_no":    o.TradeNo,
		"name":        o.Name,
		"amount":      epay.FormatCents(o.Amount),
		"pay_amount":  epay.FormatCents(o.PayAmount),
		"state":       state,
		"created_at":  o.CreatedAt,
		"expires_at":  o.ExpiresAt,
		"paid_at":     o.PaidAt,
		"server_time": store.Ms(now),
		"site_name":   site.Name,
		"has_return":  o.ReturnURL != "" && state == store.OrderPaid && now.Sub(store.Time(o.PaidAt)) <= ReturnWindow,
	}
	if state == store.OrderPending {
		if cf, _ := c.confirm.Confirmer(o.Channel); cf != nil {
			c.upstreamGuide(r, o, cf, res)
		} else if ch, _, ok := c.channels.ByKey(o.Channel); !ok {
			slog.Error("订单的收款渠道不存在", "trade_no", o.TradeNo, "channel", o.Channel)
		} else if g, err := ch.Guide(r.Context(), o); err != nil {
			slog.Error("读取收款指引失败", "trade_no", o.TradeNo, "channel", o.Channel, "err", err)
		} else {
			res["guide"] = g
		}
	}
	jsonOut(w, http.StatusOK, res)
}

type payType struct {
	Type  string `json:"type"`
	Label string `json:"label"`
}

// upstreamGuide 给上游渠道的订单补上可选的支付方式；已经向上游预下单的，给出付款信息。
func (c *cashier) upstreamGuide(r *http.Request, o *store.Order, cf channel.Confirmer, res map[string]any) {
	types := cf.PayTypes()
	opts := make([]payType, len(types))
	for i, t := range types {
		opts[i] = payType{Type: t, Label: channel.TypeLabel(t)}
	}
	res["pay_types"], res["pay_type"] = opts, o.PayType
	up, err := c.confirm.Payment(r.Context(), o.ID)
	if err != nil {
		slog.Error("读取预下单记录失败", "trade_no", o.TradeNo, "err", err)
		return
	}
	if up != nil && up.PayType == o.PayType {
		res["guide"] = upstreamGuide(up)
	}
}

func upstreamGuide(up *store.UpstreamPayment) channel.Guide {
	g := channel.Guide{App: channel.TypeLabel(up.PayType)}
	switch up.InfoType {
	case store.InfoQRCode:
		g.QRCode = up.Info
	case store.InfoJump, store.InfoURLScheme:
		g.PayURL = up.Info
	case store.InfoSubmit:
		g.Submit = true
	}
	return g
}

// pay 是付款人在收银台选了支付方式：向上游预下单，返回付款信息。
func (c *cashier) pay(w http.ResponseWriter, r *http.Request) {
	o := c.load(w, r)
	if o == nil {
		return
	}
	var in struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil || in.Type == "" {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "请选择支付方式"})
		return
	}
	if cf, _ := c.confirm.Confirmer(o.Channel); cf == nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "这个订单不用选择支付方式"})
		return
	} else if !slices.Contains(cf.PayTypes(), in.Type) {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "不支持这种支付方式"})
		return
	}
	up, err := c.confirm.Prepare(r.Context(), o, in.Type, c.baseURL(r))
	switch {
	case errors.Is(err, confirm.ErrNotPending):
		jsonOut(w, http.StatusConflict, map[string]string{"error": "订单已经不是待支付状态，请刷新页面"})
	case err != nil:
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "支付通道暂时不可用，请稍后重试或换一种支付方式"})
	default:
		jsonOut(w, http.StatusOK, map[string]any{"guide": upstreamGuide(up), "pay_type": up.PayType})
	}
}

// goPay 给“去付款”按钮一个地址：上游只给 HTML 表单时现签一个跳转地址，其余用预下单拿到的链接。
func (c *cashier) goPay(w http.ResponseWriter, r *http.Request) {
	o := c.load(w, r)
	if o == nil {
		return
	}
	cf, _ := c.confirm.Confirmer(o.Channel)
	if cf == nil || order.Effective(o, time.Now()) != store.OrderPending {
		jsonOut(w, http.StatusConflict, map[string]string{"error": "订单已经不是待支付状态，请刷新页面"})
		return
	}
	up, err := c.confirm.Payment(r.Context(), o.ID)
	if err != nil || up == nil || up.PayType != o.PayType {
		jsonOut(w, http.StatusConflict, map[string]string{"error": "请先选择支付方式"})
		return
	}
	if up.InfoType != store.InfoSubmit {
		jsonOut(w, http.StatusOK, map[string]any{"url": up.Info})
		return
	}
	u, err := cf.SubmitURL(r.Context(), o, c.baseURL(r))
	if err != nil {
		slog.Error("生成上游跳转地址失败", "trade_no", o.TradeNo, "err", err)
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "支付通道暂时不可用，请稍后重试"})
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"url": u})
}

// paid 是付款人点了"我已付款"：请求尽快查一轮账（上游渠道是查一次这个订单）。
func (c *cashier) paid(w http.ResponseWriter, r *http.Request) {
	o := c.load(w, r)
	if o == nil {
		return
	}
	if order.Effective(o, time.Now()) == store.OrderPending || o.Status == store.OrderExpired {
		if cf, _ := c.confirm.Confirmer(o.Channel); cf != nil {
			c.confirm.Kick(o.ID)
		} else {
			c.pollers.Kick(o.Channel)
		}
	}
	jsonOut(w, http.StatusOK, map[string]any{})
}

// returnURL 给出跳回商户的地址（带签名参数，V2 订单用此刻的时间戳签名）。付款超过 5 分钟后不再给。
func (c *cashier) returnURL(w http.ResponseWriter, r *http.Request) {
	o := c.load(w, r)
	if o == nil {
		return
	}
	now := time.Now()
	if o.Status != store.OrderPaid || o.ReturnURL == "" || now.Sub(store.Time(o.PaidAt)) > ReturnWindow {
		jsonOut(w, http.StatusOK, map[string]any{"url": ""})
		return
	}
	p, err := c.signer.Params(r.Context(), o, now)
	if err != nil {
		slog.Error("生成回跳参数失败", "trade_no", o.TradeNo, "err", err)
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "生成回跳地址失败"})
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"url": notify.AppendQuery(o.ReturnURL, p)})
}

// registerDev 注册演示渠道的模拟付款接口（只有 TILLBOX_CHANNEL=fake 时才有）。
func registerDev(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("POST /web-api/dev/pay", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TradeNo   string `json:"trade_no"`
			Amount    string `json:"amount"`     // 可选：按这个金额付（模拟付错金额）
			ChannelID int64  `json:"channel_id"` // 可选：不给订单号时付到哪个演示渠道，默认第一个
			Payer     string `json:"payer"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
			return
		}
		amount := int64(0)
		var demo *fake.Channel
		if in.TradeNo != "" {
			o, err := d.Orders.ByTradeNo(r.Context(), in.TradeNo)
			if err != nil {
				jsonOut(w, http.StatusNotFound, map[string]string{"error": "订单不存在"})
				return
			}
			amount = o.PayAmount
			inst, _, _ := d.Channels.ByKey(o.Channel)
			if demo, _ = inst.(*fake.Channel); demo == nil {
				jsonOut(w, http.StatusBadRequest, map[string]string{"error": "这个订单不是演示渠道的订单"})
				return
			}
		} else {
			for _, c := range d.Channels.Demos() {
				if in.ChannelID == 0 || c.Row().ID == in.ChannelID {
					demo = c
					break
				}
			}
			if demo == nil {
				jsonOut(w, http.StatusBadRequest, map[string]string{"error": "没有这个演示渠道"})
				return
			}
		}
		if in.Amount != "" {
			c, ok := epay.ParseMoney(in.Amount)
			if !ok {
				jsonOut(w, http.StatusBadRequest, map[string]string{"error": "金额不对"})
				return
			}
			amount = c
		}
		if amount <= 0 {
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": "要给订单号或金额"})
			return
		}
		payer := in.Payer
		if payer == "" {
			payer = "演示付款人(demo***@example.com)"
		}
		id := demo.Pay(amount, time.Now(), payer)
		d.Pollers.Kick(demo.Key())
		jsonOut(w, http.StatusOK, map[string]any{"external_id": id, "amount": epay.FormatCents(amount)})
	})
}
