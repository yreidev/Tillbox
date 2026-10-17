package ledger

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/channel"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// 查账节奏里不开放设置的部分。
const (
	Overlap          = 5 * time.Minute // 和上一轮窗口重叠，靠流水号去重
	FirstLookback    = 10 * time.Minute
	MaxLookback      = 30 * 24 * time.Hour // 停机太久时最多往回补这么多
	AuthBackoff      = 15 * time.Minute
	MaxBackoff       = 10 * time.Minute
	MaxBackfillRetry = time.Hour // 补扫失败后最多隔这么久再试
	notReadyRetry    = 30 * time.Second
	statePrefix      = "ledger_state:" // 后面接渠道实例键
)

// 补扫：每分钟回看 10 分钟、每小时回看 6 小时、每天回看 7 天。
// 第一档并进主窗口一起拉；后两档和停机太久时主窗口没覆盖到的区间进补扫队列，
// 每轮在主窗口之后最多拉一段，补扫慢了或失败了都不耽误最近付款的确认。
var backfills = []struct {
	Every, Lookback time.Duration
}{
	{time.Minute, 10 * time.Minute},
	{time.Hour, 6 * time.Hour},
	{24 * time.Hour, 7 * 24 * time.Hour},
}

// CatchUp 是补扫队列里"停机太久时主窗口没覆盖到的区间"的档位。
const CatchUp = -1

// Job 是补扫队列里的一项：从 From 拉到 To，From 随进度往后推。
type Job struct {
	Tier      int   `json:"tier"` // 补扫档位，CatchUp 表示停机补拉
	From      int64 `json:"from"`
	To        int64 `json:"to"`
	StartedAt int64 `json:"started_at"` // 排进队列的时刻；做完后记为这一档上次补扫的时刻
}

// State 是存在库里的查账进度，重启后从这里恢复。
type State struct {
	LastEnd          int64    `json:"last_end"`      // 主窗口上一轮成功查到的结束时刻
	LastBackfill     [3]int64 `json:"last_backfill"` // 三档补扫上次（已做完的那次）开始的时刻
	Backlog          []Job    `json:"backlog"`       // 补扫队列，按顺序逐段拉
	LastRunAt        int64    `json:"last_run_at"`   // 上一轮结束的时刻
	LastOKAt         int64    `json:"last_ok_at"`
	LastError        string   `json:"last_error"`
	Failures         int      `json:"failures"`
	BackoffUntil     int64    `json:"backoff_until"`
	BackfillError    string   `json:"backfill_error"`
	BackfillFailures int      `json:"backfill_failures"`
	BackfillRetryAt  int64    `json:"backfill_retry_at"`
}

// Status 是给后台看的查账状态。
type Status struct {
	State
	Running         bool   `json:"running"`
	Active          bool   `json:"active"`
	NextAt          int64  `json:"next_at"`
	Source          string `json:"source"`           // 渠道实例键
	ChannelID       int64  `json:"channel_id"`       // 渠道编号
	BacklogSegments int    `json:"backlog_segments"` // 补扫队列还剩几段要拉
}

type Poller struct {
	db       *bun.DB
	ledger   *Ledger
	source   channel.LedgerSource
	settings *settings.Service
	kick     chan struct{}
	// Now 可以在测试里替换。
	Now func() time.Time

	mu      sync.Mutex
	state   State
	running bool
	active  bool
	nextAt  time.Time
	loaded  bool
}

func NewPoller(db *bun.DB, l *Ledger, src channel.LedgerSource, st *settings.Service) *Poller {
	return &Poller{db: db, ledger: l, source: src, settings: st, kick: make(chan struct{}, 1), Now: time.Now}
}

// Key 是这个查账任务对应的渠道实例键。
func (p *Poller) Key() string { return p.source.Key() }

// Kick 请求尽快查一轮（收银台"我已付款"）。多次请求合并成一轮，两轮间隔仍不少于最小间隔。
func (p *Poller) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *Poller) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := p.source.Key()
	st := Status{State: p.state, Running: p.running, Active: p.active, NextAt: store.Ms(p.nextAt), Source: key, ChannelID: store.ChannelID(key)}
	st.Backlog = slices.Clone(p.state.Backlog)
	span := p.source.MaxSpan().Milliseconds()
	for _, j := range st.Backlog {
		st.BacklogSegments += int((j.To - j.From + span - 1) / span)
	}
	return st
}

func (p *Poller) load(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded {
		return nil
	}
	if _, err := p.settings.Raw(ctx, statePrefix+p.source.Key(), &p.state); err != nil {
		return err
	}
	p.loaded = true
	return nil
}

func (p *Poller) save(ctx context.Context) error {
	p.mu.Lock()
	st := p.state
	p.mu.Unlock()
	return p.settings.SaveRaw(ctx, statePrefix+p.source.Key(), st)
}

// isActive 判断要不要高频查账：有没过期的待付订单，或者刚结束（收尾期内）的未付订单。
func (p *Poller) isActive(ctx context.Context, pay settings.Payment, now time.Time) (bool, error) {
	tail := max(time.Minute, time.Duration(pay.SafetyDelaySecond+pay.ActiveInterval)*time.Second)
	since := store.Ms(now.Add(-tail))
	return p.db.NewSelect().Model((*store.Order)(nil)).
		Where("channel = ?", p.source.Key()).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.WhereGroup(" OR ", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("status IN (?)", bun.List([]string{store.OrderPending, store.OrderExpired})).Where("expires_at > ?", since)
			}).WhereGroup(" OR ", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("status = ?", store.OrderClosed).Where("ended_at > ?", since)
			})
		}).Exists(ctx)
}

// Next 计算下一轮的时间。kicked 为真时尽量提前，但不早于上一轮结束 + 最小间隔。
func (p *Poller) Next(ctx context.Context, kicked bool) (time.Time, error) {
	now := p.Now()
	pay, err := p.settings.Payment(ctx)
	if err != nil {
		return now.Add(notReadyRetry), err
	}
	active, err := p.isActive(ctx, pay, now)
	if err != nil {
		return now.Add(notReadyRetry), err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = active
	last := store.Time(p.state.LastRunAt)
	interval := time.Duration(pay.NormalInterval) * time.Second
	if active {
		interval = time.Duration(pay.ActiveInterval) * time.Second
	}
	next := last.Add(interval)
	if kicked {
		next = last.Add(time.Duration(pay.MinInterval) * time.Second)
	}
	if p.state.BackoffUntil > 0 {
		if b := store.Time(p.state.BackoffUntil); b.After(next) {
			next = b
		}
	}
	if next.Before(now) {
		next = now
	}
	p.nextAt = next
	return next, nil
}

// Run 一直查账，直到 ctx 结束。
func (p *Poller) Run(ctx context.Context) {
	if err := p.load(ctx); err != nil {
		slog.Error("读取查账进度失败", "channel", p.source.Key(), "err", err)
	}
	kicked := false
	for {
		next, err := p.Next(ctx, kicked)
		if err != nil && ctx.Err() == nil {
			slog.Warn("计算查账时间失败", "channel", p.source.Key(), "err", err)
		}
		// 查账节奏可能因为新订单变快，所以最多睡 2 秒就重新算一次。
		wait := min(next.Sub(p.Now()), 2*time.Second)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-p.kick:
			timer.Stop()
			kicked = true
			continue
		case <-timer.C:
		}
		if p.Now().Before(next) {
			continue
		}
		kicked = false
		if err := p.RunOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("查账失败", "channel", p.source.Key(), "err", err)
		}
	}
}

// RunOnce 查一轮：先对账上一轮留下的流水；再拉主窗口（从上一轮结束往前重叠 5 分钟，每分钟一次回看 10 分钟，
// 最多一段）；主窗口成功后，补扫队列拉一段。
func (p *Poller) RunOnce(ctx context.Context) error {
	if err := p.load(ctx); err != nil {
		return err
	}
	if err := p.source.Ready(ctx); err != nil {
		p.mu.Lock()
		p.state.LastRunAt = store.Ms(p.Now())
		p.state.BackoffUntil = store.Ms(p.Now().Add(notReadyRetry))
		p.state.LastError = err.Error()
		p.mu.Unlock()
		return nil
	}
	pay, err := p.settings.Payment(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.running = true
	st := p.state
	st.Backlog = slices.Clone(st.Backlog)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
	}()

	key := p.source.Key()
	now := p.Now()
	end := now.Add(-time.Duration(pay.SafetyDelaySecond) * time.Second).Truncate(time.Second)
	span := p.source.MaxSpan()
	start := end.Add(-FirstLookback)
	if st.LastEnd > 0 {
		start = store.Time(st.LastEnd).Add(-Overlap)
	}
	minuteDue := st.LastBackfill[0] == 0 || now.Sub(store.Time(st.LastBackfill[0])) >= backfills[0].Every
	if s := end.Add(-backfills[0].Lookback); minuteDue && s.Before(start) {
		start = s
	}
	if s := end.Add(-MaxLookback); start.Before(s) {
		start = s
	}
	// 主窗口只拉最近一段，更早的（停机太久）排进补扫队列。
	var gap *Job
	if s := end.Add(-span); start.Before(s) {
		gap = &Job{Tier: CatchUp, From: store.Ms(start), To: store.Ms(s), StartedAt: store.Ms(now)}
		start = s
	}

	var res Result
	add := func(r Result) {
		res.New += r.New
		res.Matched += r.Matched
		res.Anomalies += r.Anomalies
		res.Conflicts += r.Conflicts
	}
	r, runErr := p.ledger.MatchPending(ctx, key)
	add(r)
	if runErr == nil {
		var entries []channel.Entry
		if entries, runErr = p.source.Fetch(ctx, start, end); runErr == nil {
			r, runErr = p.ledger.Ingest(ctx, key, entries)
			add(r)
		}
	}
	if runErr == nil {
		st.LastEnd = store.Ms(end)
		if minuteDue {
			st.LastBackfill[0] = store.Ms(now)
		}
		// 长期停机后旧队列可能已经超过渠道的回看范围；丢弃过期段，未完成的档位会重新排队。
		oldest := store.Ms(end.Add(-MaxLookback))
		st.Backlog = slices.DeleteFunc(st.Backlog, func(j Job) bool { return j.To <= oldest })
		for i := range st.Backlog {
			st.Backlog[i].From = max(st.Backlog[i].From, oldest)
		}
		if gap != nil {
			st.Backlog = append([]Job{*gap}, st.Backlog...)
		}
		for i := 1; i < len(backfills); i++ {
			b := backfills[i]
			due := st.LastBackfill[i] == 0 || now.Sub(store.Time(st.LastBackfill[i])) >= b.Every
			queued := slices.ContainsFunc(st.Backlog, func(j Job) bool { return j.Tier == i })
			if due && !queued {
				st.Backlog = append(st.Backlog, Job{Tier: i, From: store.Ms(end.Add(-b.Lookback)), To: store.Ms(end), StartedAt: store.Ms(now)})
			}
		}
		// 停机补拉最先，其余回看短的先做；各项有自己的进度，插队不会丢进度。
		slices.SortStableFunc(st.Backlog, func(a, b Job) int { return a.Tier - b.Tier })
	}

	// 补扫：主窗口成功、队列不空、没在补扫退避期时拉一段。
	var fillErr error
	filled := false
	if runErr == nil && len(st.Backlog) > 0 && !now.Before(store.Time(st.BackfillRetryAt)) {
		filled = true
		j := &st.Backlog[0]
		s := store.Time(j.From)
		e := s.Add(span)
		if to := store.Time(j.To); e.After(to) {
			e = to
		}
		var entries []channel.Entry
		if entries, fillErr = p.source.Fetch(ctx, s, e); fillErr == nil {
			r, fillErr = p.ledger.Ingest(ctx, key, entries)
			add(r)
		}
		if fillErr == nil {
			j.From = store.Ms(e)
			if j.From >= j.To {
				if j.Tier != CatchUp {
					st.LastBackfill[j.Tier] = j.StartedAt
				}
				st.Backlog = st.Backlog[1:]
			}
		}
	}

	finished := p.Now()
	p.mu.Lock()
	p.state.LastRunAt = store.Ms(finished)
	if runErr == nil {
		p.state.LastEnd = st.LastEnd
		p.state.LastBackfill = st.LastBackfill
		p.state.Backlog = st.Backlog
		p.state.LastOKAt = store.Ms(finished)
		p.state.LastError = ""
		p.state.Failures = 0
		p.state.BackoffUntil = 0
	} else {
		p.state.Failures++
		p.state.LastError = runErr.Error()
		p.state.BackoffUntil = store.Ms(finished.Add(backoff(runErr, pay, p.state.Failures)))
	}
	if filled {
		if fillErr == nil {
			p.state.BackfillError = ""
			p.state.BackfillFailures = 0
			p.state.BackfillRetryAt = 0
		} else {
			p.state.BackfillFailures++
			p.state.BackfillError = fillErr.Error()
			p.state.BackfillRetryAt = store.Ms(finished.Add(min(time.Minute<<min(p.state.BackfillFailures-1, 10), MaxBackfillRetry)))
			// 限流和凭据错误是整个接口的问题，主窗口也一起等。
			var ra *channel.RetryAfterError
			if errors.As(fillErr, &ra) || errors.Is(fillErr, channel.ErrAuth) || errors.Is(fillErr, channel.ErrNotConfigured) {
				p.state.BackoffUntil = store.Ms(finished.Add(backoff(fillErr, pay, 1)))
			}
		}
	}
	p.mu.Unlock()
	if res.New > 0 || res.Matched > 0 || res.Anomalies > 0 {
		slog.Info("查账完成", "channel", key, "new", res.New, "matched", res.Matched, "anomalies", res.Anomalies, "conflicts", res.Conflicts)
	}
	if fillErr != nil {
		slog.Warn("补扫失败", "channel", key, "err", fillErr)
	}
	if err := p.save(ctx); err != nil {
		return err
	}
	return runErr
}

// backoff 是第 failures 次连续失败后要等多久：按活跃间隔指数增长，有上限；
// 服务端要求等更久时听它的；凭据或配置错误至少等 AuthBackoff。
func backoff(err error, pay settings.Payment, failures int) time.Duration {
	d := min(time.Duration(pay.ActiveInterval)*time.Second<<min(failures-1, 10), MaxBackoff)
	var ra *channel.RetryAfterError
	if errors.As(err, &ra) && ra.After > d {
		d = ra.After
	}
	if errors.Is(err, channel.ErrAuth) || errors.Is(err, channel.ErrNotConfigured) {
		d = max(d, AuthBackoff)
	}
	return d
}

// ResetBackoff 清掉退避（例如改了渠道配置之后），下一轮马上查。
func (p *Poller) ResetBackoff(ctx context.Context) error {
	if err := p.load(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	p.state.BackoffUntil = 0
	p.state.Failures = 0
	p.state.LastError = ""
	p.state.BackfillRetryAt = 0
	p.state.BackfillFailures = 0
	p.state.BackfillError = ""
	p.mu.Unlock()
	if err := p.save(ctx); err != nil {
		return err
	}
	p.Kick()
	return nil
}
