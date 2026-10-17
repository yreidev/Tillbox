package ledger

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/channel"
	"tillbox/internal/settings"
)

// Pollers 管理每个推断型渠道的查账任务，进度、退避、补扫队列各自独立。
type Pollers struct {
	db       *bun.DB
	ledger   *Ledger
	settings *settings.Service

	mu    sync.Mutex
	now   func() time.Time
	list  []*Poller // 按渠道编号升序
	ctx   context.Context
	stops map[string]context.CancelFunc
}

func NewPollers(db *bun.DB, l *Ledger, st *settings.Service) *Pollers {
	return &Pollers{db: db, ledger: l, settings: st, now: time.Now, stops: map[string]context.CancelFunc{}}
}

// SetNow 替换全部查账任务的"现在"（测试用），之后新建的任务也用它。
func (ps *Pollers) SetNow(f func() time.Time) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.now = f
	for _, p := range ps.list {
		p.Now = f
	}
}

// Sync 按当前的推断型渠道增减查账任务：新渠道建任务，删掉的渠道停掉任务，已有的保留进度。
func (ps *Pollers) Sync(srcs []channel.LedgerSource) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	old := make(map[string]*Poller, len(ps.list))
	for _, p := range ps.list {
		old[p.Key()] = p
	}
	list := make([]*Poller, 0, len(srcs))
	for _, src := range srcs {
		p, ok := old[src.Key()]
		delete(old, src.Key())
		if !ok || p.source != src {
			// start 会先停掉同一渠道的旧任务。
			p = NewPoller(ps.db, ps.ledger, src, ps.settings)
			p.Now = ps.now
			if ps.ctx != nil {
				ps.start(p)
			}
		}
		list = append(list, p)
	}
	for key := range old {
		if stop := ps.stops[key]; stop != nil {
			stop()
			delete(ps.stops, key)
		}
	}
	ps.list = list
}

func (ps *Pollers) start(p *Poller) {
	if stop := ps.stops[p.Key()]; stop != nil {
		stop()
	}
	ctx, cancel := context.WithCancel(ps.ctx)
	ps.stops[p.Key()] = cancel
	go p.Run(ctx)
}

// Run 跑全部查账任务，之后 Sync 新建的任务也自动开始，直到 ctx 结束。
func (ps *Pollers) Run(ctx context.Context) {
	ps.mu.Lock()
	ps.ctx = ctx
	for _, p := range ps.list {
		ps.start(p)
	}
	ps.mu.Unlock()
	<-ctx.Done()
}

// All 返回全部查账任务，按渠道编号升序。
func (ps *Pollers) All() []*Poller {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return append([]*Poller(nil), ps.list...)
}

// Get 按渠道实例键取查账任务，不是推断型渠道时返回 nil。
func (ps *Pollers) Get(key string) *Poller {
	for _, p := range ps.All() {
		if p.Key() == key {
			return p
		}
	}
	return nil
}

// Kick 请求某个渠道尽快查一轮账。
func (ps *Pollers) Kick(key string) {
	if p := ps.Get(key); p != nil {
		p.Kick()
	}
}

// Keys 返回全部推断型渠道的实例键。
func (ps *Pollers) Keys() []string {
	var out []string
	for _, p := range ps.All() {
		out = append(out, p.Key())
	}
	return out
}

// Statuses 返回全部查账任务的状态。
func (ps *Pollers) Statuses() []Status {
	var out []Status
	for _, p := range ps.All() {
		out = append(out, p.Status())
	}
	return out
}

// RunOnce 让每个查账任务各查一轮（测试和后台手动触发用），返回遇到的错误。
func (ps *Pollers) RunOnce(ctx context.Context) error {
	var errs []error
	for _, p := range ps.All() {
		if err := p.RunOnce(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ResetBackoff 清掉某个渠道的查账退避，下一轮马上查。
func (ps *Pollers) ResetBackoff(ctx context.Context, key string) error {
	if p := ps.Get(key); p != nil {
		return p.ResetBackoff(ctx)
	}
	return nil
}
