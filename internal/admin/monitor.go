package admin

import (
	"context"
	"net/http"
	"time"

	"tillbox/internal/confirm"
	"tillbox/internal/epay"
	"tillbox/internal/ledger"
	"tillbox/internal/store"
)

// healthView 是监控页上一个渠道的运行情况。
type healthView struct {
	ID       int64           `json:"id"`
	Key      string          `json:"key"`
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Enabled  bool            `json:"enabled"`
	Problem  string          `json:"problem"` // 不能收款的原因，能收款时为空
	Ledger   *ledger.Status  `json:"ledger,omitempty"`
	Upstream *confirm.Status `json:"upstream,omitempty"`
}

func (a *API) healthViews(ctx context.Context) []healthView {
	out := make([]healthView, 0)
	for _, row := range a.Channels.List() {
		v := healthView{ID: row.ID, Key: row.Key(), Name: row.Name, Type: row.Type, Enabled: row.Enabled}
		if inst, _, ok := a.Channels.Get(row.ID); ok {
			if err := inst.Ready(ctx); err != nil {
				v.Problem = err.Error()
			}
		}
		if p := a.Pollers.Get(row.Key()); p != nil {
			st := p.Status()
			v.Ledger = &st
		}
		if row.Type == store.ChannelEpay {
			st := a.Confirm.Status(row.Key())
			v.Upstream = &st
		}
		out = append(out, v)
	}
	return out
}

func (a *API) monitorPage(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	alerts, err := a.Monitor.Open(ctx)
	if err != nil {
		return err
	}
	resolved, err := a.Monitor.Resolved(ctx, 30)
	if err != nil {
		return err
	}
	state, err := a.Monitor.State(ctx)
	if err != nil {
		return err
	}
	tg, err := a.telegramView(ctx)
	if err != nil {
		return err
	}
	ok(w, map[string]any{"alerts": alerts, "resolved": resolved, "state": state, "channels": a.healthViews(ctx), "telegram": tg})
	return nil
}

// checkNow 马上检查一遍（管理员处理完问题后点“立即检查”）。
func (a *API) checkNow(w http.ResponseWriter, r *http.Request) error {
	if err := a.Monitor.Evaluate(r.Context()); err != nil {
		return err
	}
	return a.monitorPage(w, r)
}

// trendBucket 是趋势图的一格：这段时间里新建和已支付的订单（按下单时刻归格），以及商户通知的送达和失败次数。
type trendBucket struct {
	At         int64 `json:"at"`
	Created    int64 `json:"created"`
	Paid       int64 `json:"paid"`
	NotifyOK   int64 `json:"notify_ok"`
	NotifyFail int64 `json:"notify_fail"`
}

// trends 返回近 24 小时（按小时）、7 天或 30 天（按天）的趋势，按北京时间分格，不含测试订单。
func (a *API) trends(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	now := time.Now().In(epay.CST)
	var start time.Time
	var step func(time.Time) time.Time
	n := 0
	switch r.URL.Query().Get("range") {
	case "", "24h":
		n = 24
		start = now.Truncate(time.Hour).Add(-23 * time.Hour)
		step = func(t time.Time) time.Time { return t.Add(time.Hour) }
	case "7d", "30d":
		n = 7
		if r.URL.Query().Get("range") == "30d" {
			n = 30
		}
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, epay.CST).AddDate(0, 0, -(n - 1))
		step = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
	default:
		return badRequest("range 只能是 24h、7d 或 30d")
	}
	buckets := make([]trendBucket, n)
	edges := make([]int64, n+1)
	t := start
	for i := range n {
		buckets[i].At, edges[i] = store.Ms(t), store.Ms(t)
		t = step(t)
	}
	edges[n] = store.Ms(t)
	// index 找出时刻所在的格，不在范围内时返回 -1。
	index := func(ms int64) int {
		if ms < edges[0] || ms >= edges[n] {
			return -1
		}
		lo, hi := 0, n-1
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if edges[mid] <= ms {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		return lo
	}

	var orders []struct {
		CreatedAt int64  `bun:"created_at"`
		Status    string `bun:"status"`
	}
	err := a.DB.NewSelect().Model((*store.Order)(nil)).Column("created_at", "status").
		Where("is_test = ?", false).Where("created_at >= ?", edges[0]).Scan(ctx, &orders)
	if err != nil {
		return err
	}
	for _, o := range orders {
		if i := index(o.CreatedAt); i >= 0 {
			buckets[i].Created++
			if o.Status == store.OrderPaid {
				buckets[i].Paid++
			}
		}
	}
	var attempts []struct {
		CreatedAt int64 `bun:"created_at"`
		Success   bool  `bun:"success"`
	}
	err = a.DB.NewSelect().TableExpr("notify_attempts AS na").Join("JOIN orders AS o ON o.id = na.order_id").
		ColumnExpr("na.created_at, na.success").Where("o.is_test = ?", false).Where("na.created_at >= ?", edges[0]).Scan(ctx, &attempts)
	if err != nil {
		return err
	}
	for _, x := range attempts {
		if i := index(x.CreatedAt); i >= 0 {
			if x.Success {
				buckets[i].NotifyOK++
			} else {
				buckets[i].NotifyFail++
			}
		}
	}
	ok(w, map[string]any{"buckets": buckets})
	return nil
}
