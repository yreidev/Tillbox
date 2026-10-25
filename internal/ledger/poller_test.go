package ledger_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/ledger"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// recorder 记下每次拉取的窗口，可以设定失败。
type recorder struct {
	key     string
	mu      sync.Mutex
	windows [][2]time.Time
	err     error
	failIf  func(start, end time.Time) error // 只让部分窗口失败
	span    time.Duration
	entries []channel.Entry
	stray   []channel.Entry // 不管查哪段都返回，模拟渠道返回了时间不对的流水
}

func (r *recorder) Key() string            { return r.key }
func (r *recorder) MaxSpan() time.Duration { return r.span }
func (r *recorder) PayTypes() []string     { return []string{"alipay"} }
func (r *recorder) Ready(context.Context) error {
	return nil
}
func (r *recorder) Guide(context.Context, *store.Order) (channel.Guide, error) {
	return channel.Guide{}, nil
}
func (r *recorder) Fetch(_ context.Context, start, end time.Time) ([]channel.Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.windows = append(r.windows, [2]time.Time{start, end})
	if r.err != nil {
		return nil, r.err
	}
	if r.failIf != nil {
		if err := r.failIf(start, end); err != nil {
			return nil, err
		}
	}
	var out []channel.Entry
	for _, e := range r.entries {
		if !e.OccurredAt.Before(start) && e.OccurredAt.Before(end) {
			out = append(out, e)
		}
	}
	return append(out, r.stray...), nil
}
func (r *recorder) take() [][2]time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.windows
	r.windows = nil
	return w
}

func same(w [2]time.Time, start, end time.Time) bool {
	return w[0].Equal(start) && w[1].Equal(end)
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

// newPoller 建一个替代演示渠道查账的任务：流水和进度都记在演示渠道名下。
func newPoller(e *apptest.Env, src *recorder, c *clock) *ledger.Poller {
	src.key = e.ChannelKey()
	p := ledger.NewPoller(e.DB(), e.App.Ledger, src, e.App.Settings)
	p.Now = c.now
	return p
}

func TestPollerWindows(t *testing.T) {
	e := apptest.New(t)
	pay := settings.DefaultPayment()
	src := &recorder{span: 24 * time.Hour}
	c := &clock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	p := newPoller(e, src, c)
	safety := time.Duration(pay.SafetyDelaySecond) * time.Second
	run := func() [][2]time.Time {
		t.Helper()
		if err := p.RunOnce(e.Ctx); err != nil {
			t.Fatal(err)
		}
		return src.take()
	}

	// 第一轮：主窗口回看 10 分钟；补扫两档排队，先做 6 小时那档（一段）。
	first := c.t
	end := c.t.Add(-safety)
	ws := run()
	if len(ws) != 2 || !same(ws[0], end.Add(-10*time.Minute), end) || !same(ws[1], end.Add(-6*time.Hour), end) {
		t.Fatalf("第一轮窗口不对：%v", ws)
	}
	if st := p.Status(); st.BacklogSegments != 7 || st.LastBackfill[1] != store.Ms(first) {
		t.Errorf("7 天那档应排队 7 段，6 小时那档已完成：%+v", st)
	}

	// 之后每轮：主窗口从上次结束往前重叠 5 分钟；7 天那档每轮补一段，从旧到新首尾相接。
	prevEnd := end
	for i := range 7 {
		c.add(30 * time.Second)
		ws = run()
		end = c.t.Add(-safety)
		if len(ws) != 2 || !same(ws[0], prevEnd.Add(-ledger.Overlap), end) && !same(ws[0], end.Add(-10*time.Minute), end) {
			t.Fatalf("第 %d 轮主窗口不对：%v", i+2, ws)
		}
		from := first.Add(-safety).Add(-7*24*time.Hour + time.Duration(i)*24*time.Hour)
		if !same(ws[1], from, from.Add(24*time.Hour)) {
			t.Fatalf("第 %d 轮补扫应拉 %v 起的一段：%v", i+2, from, ws[1])
		}
		prevEnd = end
	}
	if st := p.Status(); st.BacklogSegments != 0 || len(st.Backlog) != 0 || st.LastBackfill[2] != store.Ms(first) {
		t.Errorf("7 天补扫应已完成：%+v", st)
	}

	// 没有补扫到期：只拉主窗口。
	c.add(10 * time.Second)
	ws = run()
	if len(ws) != 1 || !ws[0][0].Equal(prevEnd.Add(-ledger.Overlap)) {
		t.Errorf("只该拉主窗口：%v", ws)
	}

	// 每分钟那档到期：主窗口回看 10 分钟，还是一个请求。
	c.add(time.Minute)
	ws = run()
	end = c.t.Add(-safety)
	if len(ws) != 1 || !ws[0][0].Equal(end.Add(-10*time.Minute)) {
		t.Errorf("分钟补扫应并进主窗口：%v", ws)
	}

	// 1 小时后：6 小时那档到期，主窗口之后补一段。
	c.add(time.Hour)
	ws = run()
	end = c.t.Add(-safety)
	if len(ws) != 2 || !same(ws[1], end.Add(-6*time.Hour), end) {
		t.Errorf("小时补扫窗口不对：%v", ws)
	}
}

func TestPollerRestartAndLongDowntime(t *testing.T) {
	e := apptest.New(t)
	src := &recorder{span: 24 * time.Hour}
	c := &clock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	p := newPoller(e, src, c)
	if err := p.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	src.take()
	last := p.Status().LastEnd
	backlog := p.Status().Backlog

	// "重启"：新的 Poller 从库里读回进度和补扫队列。
	c.add(20 * time.Second)
	p2 := newPoller(e, src, c)
	if err := p2.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	ws := src.take()
	if len(ws) != 2 || !ws[0][0].Equal(store.Time(last).Add(-ledger.Overlap)) {
		t.Errorf("重启后应从上次结束处继续：%v", ws)
	}
	if !ws[1][0].Equal(store.Time(backlog[0].From)) {
		t.Errorf("重启后补扫应从队列进度继续：%v，队列 %+v", ws[1], backlog)
	}

	// 停机 40 天：主窗口只拉最近一天，马上能确认新付款；更早的最多回看 30 天，排在补扫队列最前面逐段补。
	c.add(40 * 24 * time.Hour)
	p3 := newPoller(e, src, c)
	if err := p3.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	ws = src.take()
	end := ws[0][1]
	if len(ws) != 2 || !same(ws[0], end.Add(-24*time.Hour), end) {
		t.Fatalf("主窗口应只拉最近一段：%v", ws)
	}
	if !same(ws[1], end.Add(-ledger.MaxLookback), end.Add(-ledger.MaxLookback+24*time.Hour)) {
		t.Errorf("停机补拉应从 30 天前开始：%v", ws[1])
	}
	st := p3.Status()
	if st.Backlog[0].Tier != ledger.CatchUp || st.Backlog[1].Tier != 1 || st.Backlog[2].Tier != 2 {
		t.Errorf("补扫队列顺序应是停机补拉、6 小时、7 天：%+v", st.Backlog)
	}
	for _, j := range st.Backlog {
		if store.Time(j.From).Before(end.Add(-ledger.MaxLookback)) {
			t.Errorf("重启不能继续查询超过回看上限的旧队列：%+v", j)
		}
	}
	if want := 28 + 1 + 7; st.BacklogSegments != want {
		t.Errorf("补扫还剩 %d 段，应为 %d", st.BacklogSegments, want)
	}
}

func TestPollerBackoff(t *testing.T) {
	e := apptest.New(t)
	src := &recorder{span: 24 * time.Hour}
	c := &clock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	p := newPoller(e, src, c)
	if err := p.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	okEnd := p.Status().LastEnd

	src.err = errors.New("网络断了")
	for i, want := range []time.Duration{8, 16, 32, 64} {
		c.add(time.Minute)
		if err := p.RunOnce(e.Ctx); err == nil {
			t.Fatal("应返回错误")
		}
		st := p.Status()
		if st.Failures != i+1 || st.LastEnd != okEnd || st.LastError == "" {
			t.Errorf("第 %d 次失败后的状态：%+v", i+1, st)
		}
		if got := store.Time(st.BackoffUntil).Sub(c.t); got != want*time.Second {
			t.Errorf("第 %d 次失败退避 %v，应为 %v", i+1, got, want*time.Second)
		}
	}
	// 退避有上限。
	for range 10 {
		c.add(time.Minute)
		_ = p.RunOnce(e.Ctx) // 一直失败，只看退避
	}
	if got := store.Time(p.Status().BackoffUntil).Sub(c.t); got != ledger.MaxBackoff {
		t.Errorf("退避上限应为 %v，实际 %v", ledger.MaxBackoff, got)
	}
	// Next 不早于退避结束。
	next, _ := p.Next(e.Ctx, true)
	if next.Before(store.Time(p.Status().BackoffUntil)) {
		t.Error("退避期间不该查账，即使有人点了已付款")
	}

	// 凭据错误至少等 15 分钟；服务端要求的等待时间更长时听它的。
	src.err = channel.ErrAuth
	c.add(time.Hour)
	_ = p.RunOnce(e.Ctx) // 预期失败，只看退避
	if got := store.Time(p.Status().BackoffUntil).Sub(c.t); got != ledger.AuthBackoff {
		t.Errorf("凭据错误退避 %v", got)
	}
	src.err = &channel.RetryAfterError{After: 30 * time.Minute, Err: errors.New("限流")}
	c.add(time.Hour)
	_ = p.RunOnce(e.Ctx) // 预期失败，只看退避
	if got := store.Time(p.Status().BackoffUntil).Sub(c.t); got != 30*time.Minute {
		t.Errorf("限流退避 %v", got)
	}

	// 恢复后清零，从失败前的进度接着查，不漏。
	src.err = nil
	c.add(time.Hour)
	src.take()
	if err := p.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	st := p.Status()
	if st.Failures != 0 || st.BackoffUntil != 0 || st.LastError != "" {
		t.Errorf("恢复后状态：%+v", st)
	}
	ws := src.take()
	if ws[0][0].After(store.Time(okEnd).Add(-ledger.Overlap)) {
		t.Errorf("恢复后的窗口没覆盖失败期间：%v，上次成功到 %v", ws[0][0], store.Time(okEnd))
	}
}

// 补扫失败不影响主窗口：最近的付款照常确认，补扫自己退避重试；限流时主窗口也一起等。
func TestPollerBackfillFailure(t *testing.T) {
	e := apptest.New(t)
	src := &recorder{span: 24 * time.Hour}
	c := &clock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	p := newPoller(e, src, c)
	fillErr := errors.New("查不了这么早的流水")
	src.failIf = func(start, end time.Time) error {
		if c.t.Sub(start) > time.Hour {
			return fillErr
		}
		return nil
	}
	if err := p.RunOnce(e.Ctx); err != nil {
		t.Fatalf("补扫失败不该让这一轮失败：%v", err)
	}
	st := p.Status()
	if st.LastEnd == 0 || st.LastError != "" || st.Failures != 0 || st.BackoffUntil != 0 {
		t.Errorf("主窗口应照常推进：%+v", st)
	}
	if st.BackfillError == "" || st.BackfillFailures != 1 || store.Time(st.BackfillRetryAt).Sub(c.t) != time.Minute || st.BacklogSegments != 8 {
		t.Errorf("补扫失败后的状态：%+v", st)
	}
	src.take()

	// 补扫退避期内只拉主窗口。
	c.add(30 * time.Second)
	_ = p.RunOnce(e.Ctx) // 补扫失败不影响主窗口，这里只看拉了哪些窗口
	if ws := src.take(); len(ws) != 1 {
		t.Errorf("补扫退避期内不该补扫：%v", ws)
	}
	// 到点重试，又失败：退避翻倍。
	c.add(30 * time.Second)
	_ = p.RunOnce(e.Ctx) // 预期补扫失败，只看退避
	if ws := src.take(); len(ws) != 2 {
		t.Errorf("到点应重试补扫：%v", ws)
	}
	if st := p.Status(); st.BackfillFailures != 2 || store.Time(st.BackfillRetryAt).Sub(c.t) != 2*time.Minute {
		t.Errorf("第二次失败后的补扫退避：%+v", st)
	}

	// 补扫遇到限流：主窗口也按服务端要求等。
	fillErr = &channel.RetryAfterError{After: 20 * time.Minute, Err: errors.New("限流")}
	c.add(2 * time.Minute)
	_ = p.RunOnce(e.Ctx) // 预期限流，只看退避
	if got := store.Time(p.Status().BackoffUntil).Sub(c.t); got != 20*time.Minute {
		t.Errorf("限流时主窗口应等 20 分钟，实际 %v", got)
	}
	// 改好配置或手动催查后，两条通道都可以立即重新尝试。
	if err := p.ResetBackoff(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if st := p.Status(); st.BackoffUntil != 0 || st.Failures != 0 || st.LastError != "" || st.BackfillRetryAt != 0 || st.BackfillFailures != 0 || st.BackfillError != "" {
		t.Fatalf("手动重试应清除主窗口和补扫退避：%+v", st)
	}

	// 恢复后补扫接着原来的进度做完，错误清掉。
	src.failIf = nil
	src.take()
	if err := p.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if ws := src.take(); len(ws) != 2 {
		t.Fatalf("清除退避后应立刻重试补扫：%v", ws)
	}
	for range 10 {
		c.add(time.Minute)
		if err := p.RunOnce(e.Ctx); err != nil {
			t.Fatal(err)
		}
	}
	if st := p.Status(); st.BackfillError != "" || st.BacklogSegments != 0 {
		t.Errorf("恢复后补扫应完成：%+v", st)
	}
}

func TestPollerCadence(t *testing.T) {
	e := apptest.New(t)
	pay := settings.DefaultPayment()
	src := &recorder{span: 24 * time.Hour}
	c := &clock{t: time.Now()}
	p := newPoller(e, src, c)
	if err := p.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	last := c.t

	next, _ := p.Next(e.Ctx, false)
	if want := last.Add(time.Duration(pay.NormalInterval) * time.Second); !next.Equal(want.Truncate(time.Millisecond)) {
		t.Errorf("空闲时下一轮应在 %v，实际 %v", want, next)
	}
	if p.Status().Active {
		t.Error("没有订单时不该是活跃")
	}

	// 有待付订单：变快。
	m := e.NewMerchant(nil)
	e.PostForm("/mapi.php", m.SignMD5(map[string]string{
		"type": "alipay", "out_trade_no": "C1", "notify_url": "https://example.com/n", "name": "x", "money": "1", "clientip": "1.1.1.1",
	}))
	next, _ = p.Next(e.Ctx, false)
	if want := last.Add(time.Duration(pay.ActiveInterval) * time.Second); !next.Equal(want.Truncate(time.Millisecond)) || !p.Status().Active {
		t.Errorf("有待付订单时下一轮应在 %v，实际 %v", want, next)
	}

	// 点了"我已付款"：不早于上一轮 + 最小间隔；已经过了就是现在。
	next, _ = p.Next(e.Ctx, true)
	if want := last.Add(time.Duration(pay.MinInterval) * time.Second); !next.Equal(want.Truncate(time.Millisecond)) {
		t.Errorf("催查时下一轮应在 %v，实际 %v", want, next)
	}
	c.add(time.Minute)
	if next, _ = p.Next(e.Ctx, true); !next.Equal(c.t) {
		t.Errorf("间隔已过时应马上查：%v", next)
	}
}

// 催查离最小间隔还有 3 秒时，要跨过调度器的 2 秒重算周期，仍在最小间隔结束后查询。
func TestPollerKickSurvivesRecheck(t *testing.T) {
	e := apptest.New(t)
	pay := settings.DefaultPayment()
	pay.MinInterval = 3
	if err := e.App.Settings.SavePayment(e.Ctx, pay); err != nil {
		t.Fatal(err)
	}
	src := &recorder{key: e.ChannelKey(), span: 24 * time.Hour}
	p := ledger.NewPoller(e.DB(), e.App.Ledger, src, e.App.Settings)
	if err := p.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	fetched := make(chan struct{}, 2)
	src.failIf = func(time.Time, time.Time) error {
		select {
		case fetched <- struct{}{}:
		default:
		}
		return nil
	}
	ctx, cancel := context.WithCancel(e.Ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	defer func() { cancel(); <-done }()
	p.Kick()
	select {
	case <-fetched:
	case <-time.After(6 * time.Second):
		t.Fatal("催查请求在重算下一轮时间时丢失，没有按最小间隔查询")
	}
}

// 时间超出查询窗口太多的流水（年份不对、差了一个时区）记成解析不了，不当成历史流水忽略，也不参与匹配；
// 稍微超出一点的照常入账。
func TestPollerEntryOutsideWindow(t *testing.T) {
	f := newFixture(t)
	o := f.order("A", 1000)
	near := f.order("B", 2000)
	old := f.entry(o.PayAmount, time.Date(1999, 1, 1, 0, 0, 0, 0, epay.CST))
	shifted := f.entry(o.PayAmount, time.Now().Add(-8*time.Hour))
	broken := channel.Entry{ExternalID: "L-broken", Direction: store.DirIn, OccurredAt: old.OccurredAt, Raw: `{"trans_amount":"x"}`, Invalid: "金额格式不对"}
	src := &recorder{key: f.ChannelKey(), span: 24 * time.Hour, stray: []channel.Entry{old, shifted, broken, f.entry(near.PayAmount, time.Now())}}
	p := ledger.NewPoller(f.DB(), f.App.Ledger, src, f.App.Settings)
	if err := p.RunOnce(f.Ctx); err != nil {
		t.Fatal(err)
	}
	if f.reload(near).Status != store.OrderPaid {
		t.Fatal("在容差内的流水应照常对账")
	}
	if f.reload(o).Status == store.OrderPaid {
		t.Fatal("时间不可靠的流水不能参与匹配")
	}
	for _, e := range []channel.Entry{old, shifted, broken} {
		var tx store.Transaction
		if err := f.DB().NewSelect().Model(&tx).Where("external_id = ?", e.ExternalID).Scan(f.Ctx); err != nil {
			t.Fatal(err)
		}
		if tx.State != store.TxnAnomaly || !ledger.Invalid(&tx) {
			t.Errorf("流水 %s 应记成解析不了，实际状态 %s", e.ExternalID, tx.State)
		}
		if d := time.Since(store.Time(tx.OccurredAt)); d < 0 || d > time.Minute {
			t.Errorf("流水 %s 的时间不可靠，应按入库时刻记，实际 %s", e.ExternalID, store.Time(tx.OccurredAt))
		}
	}
	n := 0
	for _, a := range f.anomalies() {
		if a.Kind == store.AnomalyInvalid {
			n++
		}
	}
	if n != 3 {
		t.Errorf("应记 3 条解析不了的异常，实际 %d", n)
	}
}
