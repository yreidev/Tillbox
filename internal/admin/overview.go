package admin

import (
	"net/http"
	"sort"
	"time"

	"tillbox/internal/epay"
	"tillbox/internal/store"
)

type dayStat struct {
	Date   string `json:"date"`
	Count  int64  `json:"count"`
	Amount int64  `json:"amount"`
}

type merchantStat struct {
	MerchantID int64     `json:"merchant_id"`
	Name       string    `json:"name"`
	Count      int64     `json:"count"`
	Amount     int64     `json:"amount"`
	Days       []dayStat `json:"days"`
}

// overview 是概览：今日收款、待处理数、近 N 天（7 或 30）按天和按商户的趋势。日期按北京时间分。
func (a *API) overview(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	days := 7
	if queryInt(r, "days", 7) == 30 {
		days = 30
	}
	now := time.Now()
	local := now.In(epay.CST)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, epay.CST)
	start := today.AddDate(0, 0, -(days - 1))

	var paid []struct {
		MerchantID int64 `bun:"merchant_id"`
		Amount     int64 `bun:"amount"`
		PaidAt     int64 `bun:"paid_at"`
	}
	err := a.DB.NewSelect().Model((*store.Order)(nil)).Column("merchant_id", "amount", "paid_at").
		Where("is_test = ?", false).Where("status = ?", store.OrderPaid).Where("paid_at >= ?", store.Ms(start)).Scan(ctx, &paid)
	if err != nil {
		return err
	}
	names, err := a.merchantNames(r)
	if err != nil {
		return err
	}
	dates := make([]string, days)
	index := map[string]int{}
	for i := range days {
		d := start.AddDate(0, 0, i).Format(time.DateOnly)
		dates[i] = d
		index[d] = i
	}
	newDays := func() []dayStat {
		ds := make([]dayStat, days)
		for i, d := range dates {
			ds[i].Date = d
		}
		return ds
	}
	total := newDays()
	per := map[int64]*merchantStat{}
	for _, p := range paid {
		i, ok := index[store.Time(p.PaidAt).In(epay.CST).Format(time.DateOnly)]
		if !ok {
			continue
		}
		total[i].Count++
		total[i].Amount += p.Amount
		m := per[p.MerchantID]
		if m == nil {
			m = &merchantStat{MerchantID: p.MerchantID, Name: names[p.MerchantID], Days: newDays()}
			per[p.MerchantID] = m
		}
		m.Count++
		m.Amount += p.Amount
		m.Days[i].Count++
		m.Days[i].Amount += p.Amount
	}
	merchants := make([]*merchantStat, 0, len(per))
	for _, m := range per {
		merchants = append(merchants, m)
	}
	sort.Slice(merchants, func(i, j int) bool { return merchants[i].Amount > merchants[j].Amount })

	nowMs := store.Ms(now)
	pending, err := a.DB.NewSelect().Model((*store.Order)(nil)).
		Where("is_test = ?", false).
		Where("status = ?", store.OrderPending).Where("expires_at > ?", nowMs).Count(ctx)
	if err != nil {
		return err
	}
	anomalies, err := a.DB.NewSelect().Model((*store.Anomaly)(nil)).Where("status = ?", store.AnomalyOpen).Count(ctx)
	if err != nil {
		return err
	}
	dead, err := a.DB.NewSelect().Model((*store.Notification)(nil)).Where("status = ?", store.NotifyDead).Count(ctx)
	if err != nil {
		return err
	}
	retrying, err := a.DB.NewSelect().Model((*store.Notification)(nil)).
		Where("status = ?", store.NotifyPending).Where("attempts > 0").Count(ctx)
	if err != nil {
		return err
	}
	alerts, err := a.Monitor.OpenCount(ctx)
	if err != nil {
		return err
	}
	ok(w, map[string]any{
		"today":                  total[days-1],
		"days":                   total,
		"merchants":              merchants,
		"pending_orders":         pending,
		"open_anomalies":         anomalies,
		"dead_notifications":     dead,
		"retrying_notifications": retrying,
		"open_alerts":            alerts,
		"ledgers":                a.enabledLedgers(),
	})
	return nil
}
