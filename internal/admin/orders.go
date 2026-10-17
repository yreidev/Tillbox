package admin

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/epay"
	"tillbox/internal/order"
	"tillbox/internal/store"
)

// orderView 是给后台看的订单：带上按到期时间算出的实际状态和商户名称。
type orderView struct {
	store.Order
	State        string `json:"state"`
	MerchantName string `json:"merchant_name"`
	ChannelName  string `json:"channel_name"`
}

// orderFilter 按查询参数筛选订单。
func (a *API) orderFilter(r *http.Request, q *bun.SelectQuery) *bun.SelectQuery {
	v := r.URL.Query()
	if v.Get("is_test") == "true" || v.Get("is_test") == "false" {
		q = q.Where("o.is_test = ?", v.Get("is_test") == "true")
	}
	now := store.Ms(time.Now())
	if id := queryInt64(r, "merchant_id"); id > 0 {
		q = q.Where("o.merchant_id = ?", id)
	}
	if id := queryInt64(r, "channel_id"); id > 0 {
		q = q.Where("o.channel = ?", store.ChannelKey(id))
	}
	switch v.Get("state") {
	case store.OrderPending:
		q = q.Where("o.status = ?", store.OrderPending).Where("o.expires_at > ?", now)
	case store.OrderExpired:
		q = q.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("o.status = ?", store.OrderExpired).
				WhereOr("o.status = ? AND o.expires_at <= ?", store.OrderPending, now)
		})
	case store.OrderPaid, store.OrderClosed, store.OrderDisputed:
		q = q.Where("o.status = ?", v.Get("state"))
	}
	if s := strings.TrimSpace(v.Get("q")); s != "" {
		q = q.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			q = q.Where("o.trade_no = ?", s).WhereOr("o.out_trade_no = ?", s).WhereOr("o.api_trade_no = ?", s)
			if c, ok := epay.ParseMoney(s); ok {
				q = q.WhereOr("o.amount = ?", c).WhereOr("o.pay_amount = ?", c)
			}
			return q
		})
	}
	if from := queryInt64(r, "from"); from > 0 {
		q = q.Where("o.created_at >= ?", from)
	}
	if to := queryInt64(r, "to"); to > 0 {
		q = q.Where("o.created_at < ?", to)
	}
	return q
}

func (a *API) merchantNames(r *http.Request) (map[int64]string, error) {
	ms, err := a.Merchants.List(r.Context())
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, m := range ms {
		names[m.ID] = m.Name
	}
	return names, nil
}

func (a *API) views(r *http.Request, os []store.Order) ([]orderView, error) {
	names, err := a.merchantNames(r)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]orderView, len(os))
	for i := range os {
		out[i] = orderView{Order: os[i], State: order.Effective(&os[i], now), MerchantName: names[os[i].MerchantID], ChannelName: a.Channels.Name(os[i].Channel)}
	}
	return out, nil
}

func (a *API) listOrders(w http.ResponseWriter, r *http.Request) error {
	limit, offset := page(r)
	var os []store.Order
	q := a.DB.NewSelect().Model(&os)
	total, err := a.orderFilter(r, q).Order("o.id DESC").Limit(int64(limit)).Offset(int64(offset)).ScanAndCount(r.Context())
	if err != nil {
		return err
	}
	items, err := a.views(r, os)
	if err != nil {
		return err
	}
	ok(w, map[string]any{"items": items, "total": total})
	return nil
}

var stateNames = map[string]string{
	store.OrderPending: "待支付", store.OrderPaid: "已支付", store.OrderExpired: "已过期",
	store.OrderClosed: "已关闭", store.OrderDisputed: "争议中",
}

func fmtTime(ms int64) string {
	if ms == 0 {
		return ""
	}
	return epay.FormatTime(store.Time(ms))
}

// exportOrders 按当前筛选条件导出 CSV（UTF-8 带 BOM，Excel 直接打开不乱码）。最多 10 万行。
func (a *API) exportOrders(w http.ResponseWriter, r *http.Request) error {
	var os []store.Order
	q := a.DB.NewSelect().Model(&os)
	if err := a.orderFilter(r, q).Order("o.id DESC").Limit(100000).Scan(r.Context()); err != nil {
		return err
	}
	items, err := a.views(r, os)
	if err != nil {
		return err
	}
	name := "orders-" + time.Now().In(epay.CST).Format("20060102-150405") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"平台订单号", "商户订单号", "商户编号", "商户", "商品名称", "订单金额", "应付金额", "实收金额", "状态", "创建时间", "到期时间", "支付时间", "支付渠道", "支付宝订单号", "付款方", "接口版本", "订单类型"})
	for _, o := range items {
		paid := ""
		if o.PaidAmount > 0 {
			paid = epay.FormatCents(o.PaidAmount)
		}
		kind := "业务"
		if o.IsTest {
			kind = "测试"
		}
		_ = cw.Write([]string{
			o.TradeNo, o.OutTradeNo, fmt.Sprint(o.MerchantID), o.MerchantName, csvSafe(o.Name),
			epay.FormatCents(o.Amount), epay.FormatCents(o.PayAmount), paid, stateNames[o.State],
			fmtTime(o.CreatedAt), fmtTime(o.ExpiresAt), fmtTime(o.PaidAt), csvSafe(o.ChannelName), o.APITradeNo, csvSafe(o.Buyer), fmt.Sprintf("V%d", o.Version), kind,
		})
	}
	cw.Flush()
	return cw.Error()
}

// csvSafe 防止表格软件把 = + - @ 开头的内容当公式执行。
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func (a *API) getOrder(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	o, err := a.Orders.ByID(r.Context(), id)
	if errors.Is(err, order.ErrNotFound) {
		return notFound("订单不存在")
	}
	if err != nil {
		return err
	}
	views, err := a.views(r, []store.Order{*o})
	if err != nil {
		return err
	}
	res := map[string]any{"order": views[0]}
	up, err := a.Confirm.Payment(r.Context(), o.ID)
	if err != nil {
		return err
	}
	if up != nil {
		res["upstream"] = up
	}
	if o.TxnID != 0 {
		var t store.Transaction
		if err := a.DB.NewSelect().Model(&t).Where("id = ?", o.TxnID).Scan(r.Context()); err == nil {
			res["transaction"] = t
		}
	}
	var n store.Notification
	if err := a.DB.NewSelect().Model(&n).Where("order_id = ?", o.ID).Scan(r.Context()); err == nil {
		res["notification"] = n
	}
	attempts := make([]store.NotifyAttempt, 0)
	if err := a.DB.NewSelect().Model(&attempts).Where("order_id = ?", o.ID).Order("id DESC").Limit(50).Scan(r.Context()); err != nil {
		return err
	}
	res["attempts"] = attempts
	anomalies := make([]store.Anomaly, 0)
	if err := a.DB.NewSelect().Model(&anomalies).Where("order_id = ?", o.ID).Order("id DESC").Scan(r.Context()); err != nil {
		return err
	}
	res["anomalies"] = anomalies
	ok(w, res)
	return nil
}

func (a *API) closeOrder(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	o, err := a.Orders.Close(r.Context(), id)
	switch {
	case errors.Is(err, order.ErrNotFound):
		return notFound("订单不存在")
	case errors.Is(err, order.ErrCannotClose):
		return badRequest("只有未支付的订单才能关闭")
	case err != nil:
		return err
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "order.close", o.TradeNo, "")
	ok(w, map[string]any{})
	return nil
}

type noteInput struct {
	Note    string `json:"note"`
	TradeNo string `json:"trade_no"`
}

func (in *noteInput) require() error {
	in.Note = strings.TrimSpace(in.Note)
	if in.Note == "" {
		return badRequest("请填写理由")
	}
	if len([]rune(in.Note)) > 500 {
		return badRequest("理由不超过 500 个字")
	}
	return nil
}

func (a *API) unlinkOrder(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in noteInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := in.require(); err != nil {
		return err
	}
	if err := a.Ledger.Unlink(r.Context(), id, a.actor(r), in.Note); err != nil {
		if errors.Is(err, order.ErrNotFound) {
			return notFound("订单不存在")
		}
		return badRequest(err.Error())
	}
	ok(w, map[string]any{})
	return nil
}
