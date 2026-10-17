// Package monitor 是后台监控：按固定规则检查各项运行状态，有问题就开告警、恢复了就关掉，
// 告警开始、恢复和持续未恢复时用 Telegram 通知管理员。规则见 docs/reliability.md。
package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/assistance"
	"tillbox/internal/channel/registry"
	"tillbox/internal/confirm"
	"tillbox/internal/ledger"
	"tillbox/internal/maintenance"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// Problem 是一次检查发现的一个问题。Key 相同的问题算同一条告警。
type Problem struct {
	Key    string
	Level  string
	Title  string
	Detail string
	Link   string
	Count  int64 // 待处理的笔数；不是计数类的问题为 0
}

// 连续失败到这个次数才告警，偶尔一次网络抖动不算。
const failureThreshold = 3

// UpstreamNotifyWindow 内收到过不合格的回调（且之后没有合格的）就告警。
const UpstreamNotifyWindow = 24 * time.Hour

var anomalyText = map[string]string{
	store.AnomalyAmbiguous: "匹配不唯一",
	store.AnomalyDuplicate: "重复付款",
	store.AnomalyLate:      "订单结束后付款",
	store.AnomalyUnderpaid: "少付",
	store.AnomalyOverpaid:  "多付",
	store.AnomalyUnmatched: "无法匹配",
	store.AnomalyConflict:  "流水内容变了",
	store.AnomalyUnlinked:  "撤销了关联",
}

// Checker 按规则检查一遍，列出现在的问题。
type Checker struct {
	DB          *bun.DB
	Settings    *settings.Service
	Channels    *registry.Registry
	Pollers     *ledger.Pollers
	Confirm     *confirm.Service
	Maintenance *maintenance.Service
	Assistance  *assistance.Service
	// Now 可以在测试里替换。
	Now func() time.Time
}

func (c *Checker) Check(ctx context.Context) ([]Problem, error) {
	now := c.Now()
	since := func(ms int64) time.Duration { return now.Sub(store.Time(ms)) }
	var out []Problem
	add := func(p Problem) { out = append(out, p) }

	pay, err := c.Settings.Payment(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range c.Channels.List() {
		if !row.Enabled {
			continue
		}
		key := row.Key()
		name := "渠道「" + row.Name + "」"
		if inst, _, ok := c.Channels.Get(row.ID); ok {
			if err := inst.Ready(ctx); err != nil {
				add(Problem{Key: "channel_ready:" + key, Level: store.AlertCritical, Title: name + "现在不能收款", Detail: err.Error(), Link: "/admin/channels"})
			}
		}
		// 经营码渠道：查账。停用的渠道不查账，不算问题。
		if p := c.Pollers.Get(key); p != nil {
			st := p.Status()
			if st.Failures >= failureThreshold {
				add(Problem{Key: "ledger_failures:" + key, Level: store.AlertCritical, Title: fmt.Sprintf("%s查账连续失败 %d 次", name, st.Failures), Detail: st.LastError, Link: "/admin/channels"})
			}
			if st.BackfillFailures >= failureThreshold {
				add(Problem{Key: "ledger_backfill:" + key, Level: store.AlertWarning, Title: fmt.Sprintf("%s历史补扫连续失败 %d 次", name, st.BackfillFailures), Detail: st.BackfillError, Link: "/admin/channels"})
			}
			limit := max(5*time.Minute, time.Duration(pay.NormalInterval)*3*time.Second)
			if st.LastOKAt > 0 && since(st.LastOKAt) > limit {
				add(Problem{Key: "ledger_stale:" + key, Level: store.AlertCritical, Title: fmt.Sprintf("%s超过 %d 分钟没有成功查账", name, int(limit/time.Minute)), Detail: "新付款可能不能及时确认到账。" + st.LastError, Link: "/admin/channels"})
			}
		}
	}
	// 上游渠道：停用后已经下的单还要查，所以停用的也看。
	for _, row := range c.Channels.List() {
		if row.Type != store.ChannelEpay {
			continue
		}
		key := row.Key()
		name := "渠道「" + row.Name + "」"
		st := c.Confirm.Status(key)
		if st.PrepayFailures >= failureThreshold {
			add(Problem{Key: "upstream_prepay:" + key, Level: store.AlertCritical, Title: fmt.Sprintf("%s向上游预下单连续失败 %d 次", name, st.PrepayFailures), Detail: st.PrepayErr, Link: "/admin/channels"})
		}
		if st.CheckFailures >= failureThreshold {
			add(Problem{Key: "upstream_check:" + key, Level: store.AlertCritical, Title: fmt.Sprintf("%s向上游查单连续失败 %d 次", name, st.CheckFailures), Detail: st.CheckErr, Link: "/admin/channels"})
		}
		if st.NotifyErrAt > st.NotifyAt && since(st.NotifyErrAt) < UpstreamNotifyWindow {
			add(Problem{Key: "upstream_notify:" + key, Level: store.AlertWarning, Title: name + "收到不合格的上游回调", Detail: st.NotifyErr, Link: "/admin/channels"})
		}
	}

	dead, err := c.DB.NewSelect().Model((*store.Notification)(nil)).Where("status = ?", store.NotifyDead).Count(ctx)
	if err != nil {
		return nil, err
	}
	if dead > 0 {
		add(Problem{Key: "notify_dead", Level: store.AlertCritical, Title: fmt.Sprintf("%d 笔订单没能通知到商户", dead), Detail: "自动通知都失败了，商户可能还没给付款人发货。修好商户接口后手动重发。", Link: "/admin/notifications?status=dead", Count: dead})
	}

	var kinds []struct {
		Kind string `bun:"kind"`
		N    int64  `bun:"n"`
	}
	err = c.DB.NewSelect().Model((*store.Anomaly)(nil)).Column("kind").ColumnExpr("COUNT(*) AS n").
		Where("status = ?", store.AnomalyOpen).Group("kind").Order("kind").Scan(ctx, &kinds)
	if err != nil {
		return nil, err
	}
	if len(kinds) > 0 {
		var total int64
		parts := make([]string, len(kinds))
		for i, k := range kinds {
			total += k.N
			label := anomalyText[k.Kind]
			if label == "" {
				label = k.Kind
			}
			parts[i] = fmt.Sprintf("%s %d 笔", label, k.N)
		}
		add(Problem{Key: "anomalies", Level: store.AlertWarning, Title: fmt.Sprintf("%d 笔流水异常待处理", total), Detail: strings.Join(parts, "，"), Link: "/admin/ledger", Count: total})
	}

	manual, err := c.DB.NewSelect().Model((*store.AssistanceCase)(nil)).Where("status = ?", "manual").Count(ctx)
	if err != nil {
		return nil, err
	}
	if manual > 0 {
		add(Problem{Key: "assistance_manual", Level: store.AlertWarning, Title: fmt.Sprintf("%d 个付款问题等人工处理", manual), Detail: "AI 核查后无法自动确认，需要看证据再决定。", Link: "/admin/assistance", Count: manual})
	}
	ai, err := c.Settings.Automation(ctx)
	if err != nil {
		return nil, err
	}
	if ai.Enabled {
		st, err := c.Assistance.Status(ctx)
		if err != nil {
			return nil, err
		}
		if st.LastError != "" {
			add(Problem{Key: "automation_error", Level: store.AlertWarning, Title: "AI 自动处理出错", Detail: st.LastError, Link: "/admin/assistance"})
		}
		if st.LastRunAt > 0 && since(st.LastRunAt) > max(10*time.Minute, time.Duration(ai.IntervalSeconds)*3*time.Second) {
			add(Problem{Key: "automation_stale", Level: store.AlertWarning, Title: "AI 自动处理超过巡检间隔没有运行", Link: "/admin/assistance"})
		}
	}

	retention, err := c.Settings.Retention(ctx)
	if err != nil {
		return nil, err
	}
	ms, err := c.Maintenance.Status(ctx)
	if err != nil {
		return nil, err
	}
	if ms.LastError != "" {
		add(Problem{Key: "maintenance_error", Level: store.AlertWarning, Title: "归档任务出错", Detail: ms.LastError, Link: "/admin/operations"})
	}
	if retention.Enabled && ms.LastOKAt > 0 && since(ms.LastOKAt) > 48*time.Hour {
		add(Problem{Key: "maintenance_stale", Level: store.AlertWarning, Title: "归档任务超过 48 小时没有成功执行", Link: "/admin/operations"})
	}
	return out, nil
}
