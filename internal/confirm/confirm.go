// Package confirm 处理确认型渠道（上游易支付）的订单：预下单、收回调、定时查单，确认后交给流水入账。
// 规则见 docs/channels.md 第 3.3 节。
package confirm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/uptrace/bun"
	"golang.org/x/sync/singleflight"

	"tillbox/internal/channel"
	"tillbox/internal/channel/registry"
	"tillbox/internal/ledger"
	"tillbox/internal/order"
	"tillbox/internal/store"
)

// 查单节奏：待支付订单每 15 秒查一次，过期后 10 分钟内每分钟查一次；付款人点“我已付款”时立即查，但同一订单至少隔 3 秒。
const (
	PendingEvery = 15 * time.Second
	ExpiredEvery = time.Minute
	ExpiredFor   = 10 * time.Minute
	KickGap      = 3 * time.Second
	tick         = 5 * time.Second
	maxPerRound  = 50
)

var (
	ErrNotConfirmer = errors.New("这个订单的渠道不是上游易支付渠道")
	ErrNotPending   = errors.New("订单已经不是待支付状态")
)

// BadNotify 是不合格的回调（验签失败、订单对不上等），回复上游 fail。
type BadNotify struct{ Err error }

func (e *BadNotify) Error() string { return e.Err.Error() }
func (e *BadNotify) Unwrap() error { return e.Err }

// Status 是一个上游渠道最近的查单和回调情况，给后台和运维巡检看。
type Status struct {
	Key         string `json:"key"`
	ChannelID   int64  `json:"channel_id"`
	CheckOKAt   int64  `json:"check_ok_at"`  // 上次查单成功
	CheckErrAt  int64  `json:"check_err_at"` // 上次查单失败
	CheckErr    string `json:"check_err"`
	Failures    int    `json:"failures"` // 连续失败的查单和预下单次数
	PrepayErrAt int64  `json:"prepay_err_at"`
	PrepayErr   string `json:"prepay_err"`
	NotifyAt    int64  `json:"notify_at"` // 上次收到合格的回调
	NotifyErrAt int64  `json:"notify_err_at"`
	NotifyErr   string `json:"notify_err"`
}

type Service struct {
	db       *bun.DB
	orders   *order.Service
	ledger   *ledger.Ledger
	channels *registry.Registry
	// Now 可以在测试里替换。
	Now func() time.Time

	prepay singleflight.Group
	kick   chan struct{}

	mu     sync.Mutex
	kicked map[int64]bool
	status map[string]*Status
}

func New(db *bun.DB, orders *order.Service, l *ledger.Ledger, channels *registry.Registry) *Service {
	return &Service{
		db: db, orders: orders, ledger: l, channels: channels, Now: time.Now,
		kick: make(chan struct{}, 1), kicked: map[int64]bool{}, status: map[string]*Status{},
	}
}

// Confirmer 返回订单所属渠道的确认型实例，不是确认型渠道时返回 nil。
func (s *Service) Confirmer(key string) (channel.Confirmer, store.Channel) {
	inst, row, ok := s.channels.ByKey(key)
	if !ok {
		return nil, row
	}
	c, _ := inst.(channel.Confirmer)
	return c, row
}

func (s *Service) record(key string, f func(st *Status, nowMs int64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status[key]
	if st == nil {
		st = &Status{Key: key, ChannelID: store.ChannelID(key)}
		s.status[key] = st
	}
	f(st, store.Ms(s.Now()))
}

// Status 返回一个渠道最近的查单和回调情况。
func (s *Service) Status(key string) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.status[key]; st != nil {
		return *st
	}
	return Status{Key: key, ChannelID: store.ChannelID(key)}
}

// Payment 返回订单的预下单结果，没有时返回 nil。
func (s *Service) Payment(ctx context.Context, orderID int64) (*store.UpstreamPayment, error) {
	var up store.UpstreamPayment
	err := s.db.NewSelect().Model(&up).Where("order_id = ?", orderID).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &up, nil
}

// Prepare 按支付方式向上游预下单。同一订单已经用这个支付方式预下单过的，直接返回原结果；
// 换了支付方式就重新预下单（上游同一订单号允许换方式）。base 是本系统的对外地址。
func (s *Service) Prepare(ctx context.Context, o *store.Order, typ, base string) (*store.UpstreamPayment, error) {
	c, row := s.Confirmer(o.Channel)
	if c == nil {
		return nil, ErrNotConfirmer
	}
	if typ == "" {
		typ = o.PayType
	}
	if !slices.Contains(c.PayTypes(), typ) {
		return nil, fmt.Errorf("渠道「%s」没有开放%s", row.Name, channel.TypeLabel(typ))
	}
	v, err, _ := s.prepay.Do(strconv.FormatInt(o.ID, 10)+":"+typ, func() (any, error) {
		cur, err := s.orders.ByID(ctx, o.ID)
		if err != nil {
			return nil, err
		}
		if order.Effective(cur, s.Now()) != store.OrderPending {
			return nil, ErrNotPending
		}
		up, err := s.Payment(ctx, cur.ID)
		if err != nil {
			return nil, err
		}
		if up != nil && up.PayType == typ && up.ChannelID == row.ID {
			return up, nil
		}
		if cur.PayType != typ {
			res, err := s.db.NewUpdate().Model((*store.Order)(nil)).Set("pay_type = ?", typ).Set("updated_at = ?", store.Ms(s.Now())).
				Where("id = ?", cur.ID).Where("status = ?", store.OrderPending).Exec(ctx)
			if err != nil {
				return nil, err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return nil, ErrNotPending
			}
			cur.PayType = typ
		}
		pay, err := c.Prepay(ctx, cur, base)
		if err != nil {
			s.record(o.Channel, func(st *Status, now int64) { st.PrepayErrAt, st.PrepayErr = now, err.Error(); st.Failures++ })
			slog.Warn("上游预下单失败", "trade_no", cur.TradeNo, "channel", cur.Channel, "type", typ, "err", err)
			return nil, err
		}
		s.record(o.Channel, func(st *Status, _ int64) { st.Failures = 0 })
		nowMs := store.Ms(s.Now())
		up = &store.UpstreamPayment{
			OrderID: cur.ID, ChannelID: row.ID, PayType: typ, TradeNo: pay.TradeNo, InfoType: pay.InfoType, Info: pay.Info,
			CreatedAt: nowMs, UpdatedAt: nowMs,
		}
		_, err = s.db.NewInsert().Model(up).
			On("CONFLICT (order_id) DO UPDATE").
			Set("channel_id = EXCLUDED.channel_id").Set("pay_type = EXCLUDED.pay_type").Set("trade_no = EXCLUDED.trade_no").
			Set("info_type = EXCLUDED.info_type").Set("info = EXCLUDED.info").Set("check_error = ''").
			Set("updated_at = EXCLUDED.updated_at").
			Exec(ctx)
		if err != nil {
			return nil, err
		}
		return up, nil
	})
	if err != nil {
		return nil, err
	}
	o.PayType = typ
	return v.(*store.UpstreamPayment), nil
}

// Notify 处理上游的回调：验签、核对订单，再交给流水确认到账。不合格的回调返回 *BadNotify。
func (s *Service) Notify(ctx context.Context, channelID int64, p map[string]string) error {
	key := store.ChannelKey(channelID)
	c, _ := s.Confirmer(key)
	if c == nil {
		return &BadNotify{Err: errors.New("渠道不存在或不是上游易支付渠道")}
	}
	bad := func(err error) error {
		s.record(key, func(st *Status, now int64) { st.NotifyErrAt, st.NotifyErr = now, err.Error() })
		slog.Warn("上游回调不合格", "channel", key, "out_trade_no", p["out_trade_no"], "err", err)
		return &BadNotify{Err: err}
	}
	paid, err := c.VerifyNotify(p)
	if err != nil {
		return bad(err)
	}
	o, err := s.orders.ByTradeNo(ctx, paid.OutTradeNo)
	if errors.Is(err, order.ErrNotFound) {
		return bad(fmt.Errorf("回调的订单号 %s 不存在", paid.OutTradeNo))
	}
	if err != nil {
		return err
	}
	if o.Channel != key {
		return bad(fmt.Errorf("订单 %s 不属于这个渠道", o.TradeNo))
	}
	if _, err := s.ledger.Confirm(ctx, o.ID, *paid); err != nil {
		return err
	}
	s.record(key, func(st *Status, now int64) { st.NotifyAt = now })
	return nil
}

// Check 向上游查一次订单，已付款就确认到账。返回是否确认了到账。
func (s *Service) Check(ctx context.Context, o *store.Order, up *store.UpstreamPayment) (bool, error) {
	c, _ := s.Confirmer(o.Channel)
	if c == nil {
		return false, ErrNotConfirmer
	}
	paid, qerr := c.Query(ctx, o, up.TradeNo)
	nowMs := store.Ms(s.Now())
	msg := ""
	if qerr != nil {
		msg = qerr.Error()
		s.record(o.Channel, func(st *Status, now int64) { st.CheckErrAt, st.CheckErr = now, msg; st.Failures++ })
	} else {
		s.record(o.Channel, func(st *Status, now int64) { st.CheckOKAt, st.Failures = now, 0 })
	}
	up.CheckedAt, up.CheckError, up.UpdatedAt = nowMs, msg, nowMs
	if _, err := s.db.NewUpdate().Model(up).Column("checked_at", "check_error", "updated_at").WherePK().Exec(ctx); err != nil {
		return false, err
	}
	if qerr != nil || paid == nil {
		return false, qerr
	}
	return s.ledger.Confirm(ctx, o.ID, *paid)
}

// Kick 请求尽快查一次这个订单（付款人点了“我已付款”）。
func (s *Service) Kick(orderID int64) {
	s.mu.Lock()
	s.kicked[orderID] = true
	s.mu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run 定时查单，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("上游查单失败", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// keys 是全部上游渠道的实例键（停用的也算：已经下的单还要查）。
func (s *Service) keys() []string {
	var keys []string
	for _, row := range s.channels.List() {
		if c, _ := s.Confirmer(row.Key()); c != nil {
			keys = append(keys, row.Key())
		}
	}
	return keys
}

// RunOnce 查一轮到期该查的订单。单个订单查单失败只记下来，不中断这一轮。
func (s *Service) RunOnce(ctx context.Context) error {
	s.mu.Lock()
	kicked := s.kicked
	s.kicked = map[int64]bool{}
	s.mu.Unlock()
	keys := s.keys()
	if len(keys) == 0 {
		return nil
	}
	now := s.Now()
	nowMs := store.Ms(now)
	var os []store.Order
	err := s.db.NewSelect().Model(&os).
		Where("channel IN (?)", bun.List(keys)).
		Where("status IN (?)", bun.List([]string{store.OrderPending, store.OrderExpired})).
		Where("expires_at > ?", nowMs-ExpiredFor.Milliseconds()).
		Order("id ASC").Limit(500).Scan(ctx)
	if err != nil {
		return err
	}
	// 被点了“我已付款”、但已经过期超过 10 分钟的订单也查一次。
	var extra []int64
	for id := range kicked {
		if !slices.ContainsFunc(os, func(o store.Order) bool { return o.ID == id }) {
			extra = append(extra, id)
		}
	}
	if len(extra) > 0 {
		var more []store.Order
		err := s.db.NewSelect().Model(&more).Where("id IN (?)", bun.List(extra)).
			Where("channel IN (?)", bun.List(keys)).
			Where("status IN (?)", bun.List([]string{store.OrderPending, store.OrderExpired})).Scan(ctx)
		if err != nil {
			return err
		}
		os = append(os, more...)
	}
	if len(os) == 0 {
		return nil
	}
	ids := make([]int64, len(os))
	for i, o := range os {
		ids[i] = o.ID
	}
	var ups []store.UpstreamPayment
	if err := s.db.NewSelect().Model(&ups).Where("order_id IN (?)", bun.List(ids)).Scan(ctx); err != nil {
		return err
	}
	byOrder := make(map[int64]*store.UpstreamPayment, len(ups))
	for i := range ups {
		byOrder[ups[i].OrderID] = &ups[i]
	}
	n := 0
	for i := range os {
		o := &os[i]
		up := byOrder[o.ID]
		if up == nil {
			continue // 还没选支付方式、没向上游下单
		}
		gap := ExpiredEvery
		if order.Effective(o, now) == store.OrderPending {
			gap = PendingEvery
		}
		if kicked[o.ID] {
			gap = KickGap
		}
		if nowMs-up.CheckedAt < gap.Milliseconds() {
			continue
		}
		if n++; n > maxPerRound {
			break
		}
		if _, err := s.Check(ctx, o, up); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("上游查单失败", "trade_no", o.TradeNo, "channel", o.Channel, "err", err)
		}
	}
	return nil
}
