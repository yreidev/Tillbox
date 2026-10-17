package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/uptrace/bun"

	"tillbox/internal/epay"
	"tillbox/internal/ledger"
	"tillbox/internal/notify"
	"tillbox/internal/order"
	"tillbox/internal/store"
)

func (a *API) listTransactions(w http.ResponseWriter, r *http.Request) error {
	limit, offset := page(r)
	v := r.URL.Query()
	ts := make([]store.Transaction, 0)
	q := a.DB.NewSelect().Model(&ts).ExcludeColumn("raw")
	if s := v.Get("state"); s != "" {
		q = q.Where("t.state = ?", s)
	}
	if d := v.Get("direction"); d != "" {
		q = q.Where("t.direction = ?", d)
	}
	if id := queryInt64(r, "channel_id"); id > 0 {
		q = q.Where("t.channel = ?", store.ChannelKey(id))
	}
	if s := strings.TrimSpace(v.Get("q")); s != "" {
		q = q.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			q = q.Where("t.external_id = ?", s).WhereOr("t.alipay_order_no = ?", s).WhereOr("t.other_account LIKE ?", "%"+s+"%")
			if c, ok := epay.ParseMoney(s); ok {
				q = q.WhereOr("t.amount = ?", c)
			}
			return q
		})
	}
	if from := queryInt64(r, "from"); from > 0 {
		q = q.Where("t.occurred_at >= ?", from)
	}
	if to := queryInt64(r, "to"); to > 0 {
		q = q.Where("t.occurred_at < ?", to)
	}
	total, err := q.Order("t.occurred_at DESC", "t.id DESC").Limit(int64(limit)).Offset(int64(offset)).ScanAndCount(r.Context())
	if err != nil {
		return err
	}
	tradeNos, err := a.tradeNosOf(r, func(add func(int64)) {
		for _, t := range ts {
			add(t.OrderID)
		}
	})
	if err != nil {
		return err
	}
	ok(w, map[string]any{"items": ts, "total": total, "trade_nos": tradeNos})
	return nil
}

// tradeNosOf 把一批订单编号换成平台订单号，给列表显示用。
func (a *API) tradeNosOf(r *http.Request, each func(add func(int64))) (map[int64]string, error) {
	var ids []int64
	each(func(id int64) {
		if id != 0 {
			ids = append(ids, id)
		}
	})
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID      int64  `bun:"id"`
		TradeNo string `bun:"trade_no"`
	}
	err := a.DB.NewSelect().Model((*store.Order)(nil)).Column("id", "trade_no").Where("id IN (?)", bun.List(ids)).Scan(r.Context(), &rows)
	for _, row := range rows {
		out[row.ID] = row.TradeNo
	}
	return out, err
}

func (a *API) linkTransaction(w http.ResponseWriter, r *http.Request) error {
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
	o, err := a.Orders.ByTradeNo(r.Context(), strings.TrimSpace(in.TradeNo))
	if errors.Is(err, order.ErrNotFound) {
		return badRequest("找不到平台订单号为 " + in.TradeNo + " 的订单")
	}
	if err != nil {
		return err
	}
	if err := a.Ledger.Link(r.Context(), id, o.ID, a.actor(r), in.Note); err != nil {
		return badRequest(err.Error())
	}
	ok(w, map[string]any{})
	return nil
}

type anomalyView struct {
	store.Anomaly
	TradeNo     string             `json:"trade_no"`
	Transaction *store.Transaction `json:"transaction,omitempty"`
}

func (a *API) listAnomalies(w http.ResponseWriter, r *http.Request) error {
	limit, offset := page(r)
	v := r.URL.Query()
	var as []store.Anomaly
	q := a.DB.NewSelect().Model(&as)
	if s := v.Get("status"); s != "" {
		q = q.Where("an.status = ?", s)
	}
	if k := v.Get("kind"); k != "" {
		q = q.Where("an.kind = ?", k)
	}
	total, err := q.Order("an.id DESC").Limit(int64(limit)).Offset(int64(offset)).ScanAndCount(r.Context())
	if err != nil {
		return err
	}
	tradeNos, err := a.tradeNosOf(r, func(add func(int64)) {
		for _, x := range as {
			add(x.OrderID)
		}
	})
	if err != nil {
		return err
	}
	var txnIDs []int64
	for _, x := range as {
		if x.TransactionID != 0 {
			txnIDs = append(txnIDs, x.TransactionID)
		}
	}
	txns := map[int64]*store.Transaction{}
	if len(txnIDs) > 0 {
		var ts []store.Transaction
		if err := a.DB.NewSelect().Model(&ts).ExcludeColumn("raw").Where("t.id IN (?)", bun.List(txnIDs)).Scan(r.Context()); err != nil {
			return err
		}
		for i := range ts {
			txns[ts[i].ID] = &ts[i]
		}
	}
	items := make([]anomalyView, len(as))
	for i, x := range as {
		items[i] = anomalyView{Anomaly: x, TradeNo: tradeNos[x.OrderID], Transaction: txns[x.TransactionID]}
	}
	open, err := a.DB.NewSelect().Model((*store.Anomaly)(nil)).Where("status = ?", store.AnomalyOpen).Count(r.Context())
	if err != nil {
		return err
	}
	ok(w, map[string]any{"items": items, "total": total, "open": open})
	return nil
}

func (a *API) resolveAnomaly(w http.ResponseWriter, r *http.Request) error {
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
	if err := a.Ledger.Resolve(r.Context(), id, a.actor(r), in.Note); err != nil {
		return badRequest(err.Error())
	}
	ok(w, map[string]any{})
	return nil
}

// ledgerStatusView 是一个推断型渠道的查账状态，带上渠道名称和是否启用。
type ledgerStatusView struct {
	ledger.Status
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (a *API) ledgerStatuses() []ledgerStatusView {
	out := make([]ledgerStatusView, 0)
	for _, st := range a.Pollers.Statuses() {
		v := ledgerStatusView{Status: st, Name: st.Source}
		if _, row, ok := a.Channels.ByKey(st.Source); ok {
			v.Name, v.Enabled = row.Name, row.Enabled
		}
		out = append(out, v)
	}
	return out
}

// enabledLedgers 只留启用的渠道（概览和运维检查用；停用的渠道不查账）。
func (a *API) enabledLedgers() []ledgerStatusView {
	out := make([]ledgerStatusView, 0)
	for _, v := range a.ledgerStatuses() {
		if v.Enabled {
			out = append(out, v)
		}
	}
	return out
}

func (a *API) ledgerStatus(w http.ResponseWriter, r *http.Request) error {
	ok(w, map[string]any{"items": a.ledgerStatuses()})
	return nil
}

// ledgerPoll 清掉退避并马上查一轮。channel_id 为 0 时所有渠道都查。
func (a *API) ledgerPoll(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		ChannelID int64 `json:"channel_id"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	for _, key := range a.Pollers.Keys() {
		if in.ChannelID == 0 || key == store.ChannelKey(in.ChannelID) {
			if err := a.Pollers.ResetBackoff(r.Context(), key); err != nil {
				return err
			}
		}
	}
	ok(w, map[string]any{})
	return nil
}

type notificationView struct {
	store.Notification
	TradeNo    string `json:"trade_no"`
	OutTradeNo string `json:"out_trade_no"`
	MerchantID int64  `json:"merchant_id"`
	NotifyURL  string `json:"notify_url"`
}

func (a *API) listNotifications(w http.ResponseWriter, r *http.Request) error {
	limit, offset := page(r)
	ns := make([]notificationView, 0)
	q := a.DB.NewSelect().
		TableExpr("notifications AS n").
		ColumnExpr("n.*").
		ColumnExpr("o.trade_no, o.out_trade_no, o.merchant_id, o.notify_url").
		Join("JOIN orders AS o ON o.id = n.order_id")
	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where("n.status = ?", s)
	}
	total, err := q.Order("n.updated_at DESC").Limit(int64(limit)).Offset(int64(offset)).ScanAndCount(r.Context(), &ns)
	if err != nil {
		return err
	}
	ok(w, map[string]any{"items": ns, "total": total})
	return nil
}

func (a *API) resendNotification(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	att, err := a.Notifier.Resend(r.Context(), id)
	if err != nil {
		if errors.Is(err, notify.ErrBusy) {
			return badRequest(err.Error())
		}
		return badRequest("重发失败：" + err.Error())
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "notify.resend", a.tradeNo(r, id), "")
	ok(w, map[string]any{"attempt": att})
	return nil
}

func (a *API) tradeNo(r *http.Request, orderID int64) string {
	o, err := a.Orders.ByID(r.Context(), orderID)
	if err != nil {
		return ""
	}
	return o.TradeNo
}
