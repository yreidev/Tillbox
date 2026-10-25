package web

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/assistance"
	"tillbox/internal/channel"
	"tillbox/internal/channel/fake"
	"tillbox/internal/channel/registry"
	"tillbox/internal/confirm"
	"tillbox/internal/epay"
	"tillbox/internal/ledger"
	"tillbox/internal/merchant"
	"tillbox/internal/notify"
	"tillbox/internal/order"
	"tillbox/internal/payroute"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// ReturnWindow 是付款后还能跳回商户的时长（和官方一样 5 分钟）。
const ReturnWindow = 5 * time.Minute

var tradeNoRe = regexp.MustCompile(`^[0-9]{19}$`)

type cashier struct {
	orders    *order.Service
	merchants *merchant.Service
	settings  *settings.Service
	channels  *registry.Registry
	pollers   *ledger.Pollers
	confirm   *confirm.Service
	signer    *notify.Signer
	db        *bun.DB
	baseURL   func(r *http.Request) string
	clientIP  func(r *http.Request) string
	limiter   assistance.Limiter // 取消订单按 IP 限次数
}

func (c *cashier) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /web-api/cashier/{trade_no}", c.get)
	mux.HandleFunc("POST /web-api/cashier/{trade_no}/paid", c.paid)
	mux.HandleFunc("POST /web-api/cashier/{trade_no}/pay", c.pay)
	mux.HandleFunc("POST /web-api/cashier/{trade_no}/go", c.goPay)
	mux.HandleFunc("POST /web-api/cashier/{trade_no}/cancel", c.cancel)
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
		cf, _ := c.confirm.Confirmer(o.Channel)
		if o.Channel == "" {
			c.chooseOptions(r, o, res)
		} else if cf != nil {
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
	// Unavailable 是这笔订单不能用这种方式的原因（超出的单笔限额），能用时为空。
	Unavailable string `json:"unavailable,omitempty"`
}

// chooseOptions 给付款人还没选支付方式的订单列出商户开放的方式。收不了款的方式标成暂时不可用，不说原因。
func (c *cashier) chooseOptions(r *http.Request, o *store.Order, res map[string]any) {
	m, err := c.merchants.Get(r.Context(), o.MerchantID)
	if err != nil {
		slog.Error("收银台读取商户失败", "trade_no", o.TradeNo, "err", err)
		return
	}
	methods := payroute.Methods(r.Context(), c.channels, m.Routes)
	opts := make([]payType, len(methods))
	for i, mt := range methods {
		opts[i] = payType{Type: mt.Type, Label: channel.TypeLabel(mt.Type)}
		var ae *channel.AmountError
		if !mt.Ready() {
			opts[i].Unavailable = "暂时不可用"
		} else if errors.As(mt.CheckAmount(o.Amount, time.Now()), &ae) {
			opts[i].Unavailable = ae.Rule()
		}
	}
	res["pay_types"], res["pay_type"] = opts, ""
}

// upstreamGuide 给第三方易支付渠道的订单补上支付方式：定了方式的只有这一种（预下单失败时可以重试），
// 升级前建的还没定方式的列出渠道开放的方式。已经预下单的给出付款信息。
func (c *cashier) upstreamGuide(r *http.Request, o *store.Order, cf channel.Confirmer, res map[string]any) {
	types := cf.PayTypes()
	if o.PayType != "" {
		types = []string{o.PayType}
	}
	opts := make([]payType, len(types))
	l, _ := cf.(channel.Limiter)
	for i, t := range types {
		opts[i] = payType{Type: t, Label: channel.TypeLabel(t)}
		var ae *channel.AmountError
		if l != nil && errors.As(l.CheckAmount(t, o.Amount, time.Now()), &ae) {
			opts[i].Unavailable = ae.Rule()
		}
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

// pay 是付款人在收银台选了支付方式：定下订单的渠道（经营码当场分配金额尾数），第三方易支付接着预下单，
// 返回付款信息。选定后不能再换，只能取消订单。
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
	if order.Effective(o, time.Now()) != store.OrderPending {
		jsonOut(w, http.StatusConflict, map[string]string{"error": "订单已经不是待支付状态，请刷新页面"})
		return
	}
	undecided := o.Channel == ""
	var mt payroute.Method
	if undecided {
		m, err := c.merchants.Get(r.Context(), o.MerchantID)
		if err != nil {
			slog.Error("收银台读取商户失败", "trade_no", o.TradeNo, "err", err)
			jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "系统繁忙，请稍后重试"})
			return
		}
		var ok bool
		if mt, ok = payroute.Find(payroute.Methods(r.Context(), c.channels, m.Routes), in.Type); !ok {
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": "不支持这种支付方式"})
			return
		}
		if !mt.Ready() {
			jsonOut(w, http.StatusConflict, map[string]string{"error": "这种支付方式暂时不可用，请换一种"})
			return
		}
	} else {
		var ok bool
		if mt, ok = payroute.Of(c.channels, o, in.Type); !ok {
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": "不支持这种支付方式"})
			return
		}
		if !slices.Contains(mt.Channel.PayTypes(), in.Type) {
			err := errUnsupportedType
			if o.PayType != "" {
				err = order.ErrChosen
			}
			c.payFailed(w, o, in.Type, false, err)
			return
		}
	}
	up, err := payroute.Choose(r.Context(), c.orders, c.confirm, o, mt, c.baseURL(r))
	if err != nil {
		c.payFailed(w, o, in.Type, undecided, err)
		return
	}
	res := map[string]any{"pay_type": o.PayType, "pay_amount": epay.FormatCents(o.PayAmount)}
	if up != nil {
		res["guide"] = upstreamGuide(up)
	} else if g, err := mt.Channel.Guide(r.Context(), o); err != nil {
		slog.Error("读取收款指引失败", "trade_no", o.TradeNo, "channel", o.Channel, "err", err)
	} else {
		res["guide"] = g
	}
	jsonOut(w, http.StatusOK, res)
}

var errUnsupportedType = errors.New("不支持这种支付方式")

// payFailed 回复选支付方式失败的原因。undecided 是付款人选之前订单还没定方式：这时失败了还能换一种。
func (c *cashier) payFailed(w http.ResponseWriter, o *store.Order, typ string, undecided bool, err error) {
	var ae *channel.AmountError
	var full *order.PoolFullError
	switch {
	case errors.Is(err, errUnsupportedType):
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, order.ErrNotPending):
		jsonOut(w, http.StatusConflict, map[string]string{"error": "订单已经不是待支付状态，请刷新页面"})
	case errors.Is(err, order.ErrChosen):
		chosen := "支付方式"
		if o.PayType != "" {
			chosen = channel.TypeLabel(o.PayType)
		}
		jsonOut(w, http.StatusConflict, map[string]string{"error": "已经选了" + chosen + "，不能再换；要换支付方式请取消订单，回到商户重新下单"})
	case errors.As(err, &ae):
		msg := ae.Message()
		if undecided {
			msg += "，请换一种支付方式"
		}
		jsonOut(w, http.StatusUnprocessableEntity, map[string]string{"error": msg})
	case errors.As(err, &full):
		jsonOut(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		slog.Warn("收银台选支付方式失败", "trade_no", o.TradeNo, "type", typ, "err", err)
		msg := "支付通道暂时不可用，请稍后重试"
		if undecided {
			// 预下单失败时选择已经撤回，付款人可以换一种
			msg = "支付通道暂时不可用，请换一种支付方式或稍后重试"
		}
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": msg})
	}
}

// cancel 是付款人在收银台取消待支付的订单（要换支付方式只能这样）。取消后再付的款进流水异常，人工核对。
func (c *cashier) cancel(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Tillbox") == "" {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "请在收银台页面操作"})
		return
	}
	if !c.limiter.Allow("cashier-cancel:"+c.clientIP(r), 20) {
		jsonOut(w, http.StatusTooManyRequests, map[string]string{"error": "操作过于频繁，请稍后再试"})
		return
	}
	o := c.load(w, r)
	if o == nil {
		return
	}
	pending := order.Effective(o, time.Now()) == store.OrderPending
	closed, err := c.orders.Close(r.Context(), o.ID)
	if errors.Is(err, order.ErrCannotClose) {
		jsonOut(w, http.StatusConflict, map[string]string{"error": "订单已经不是待支付状态，请刷新页面"})
		return
	}
	if err != nil {
		slog.Error("付款人取消订单失败", "trade_no", o.TradeNo, "err", err)
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "取消失败，请稍后重试"})
		return
	}
	if pending {
		_ = store.Audit(r.Context(), c.db, "付款人", "order.cancel", o.TradeNo, "在收银台取消了订单")
	}
	jsonOut(w, http.StatusOK, map[string]any{"state": closed.Status})
}

// goPay 给“去付款”按钮一个地址：第三方平台只给 HTML 表单时现签一个跳转地址，其余用预下单拿到的链接。
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
		slog.Error("生成第三方平台跳转地址失败", "trade_no", o.TradeNo, "err", err)
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "支付通道暂时不可用，请稍后重试"})
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"url": u})
}

// paid 是付款人点了"我已付款"：请求尽快查一轮账（第三方易支付渠道是查一次这个订单）。
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

// registerDev 注册演示渠道的模拟付款接口（只有 TILLBOX_DEMO=true 时才有）。
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
