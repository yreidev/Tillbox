// Package order 负责订单：建单（含金额尾数池）、查单、关单、过期和确认到账。
//
// 金额分配、关单、过期和确认到账都在同一把进程内的锁里做，服务按单实例运行。
package order

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/epay"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

var ErrNotFound = errors.New("订单号不存在")

// 订单版本
const (
	V1 = 1 // V1 入口创建，通知用 MD5
	V2 = 2 // V2 入口创建，通知用 RSA
)

type Service struct {
	db       *bun.DB
	settings *settings.Service
	mu       sync.Mutex
	// Now 可以在测试里替换。
	Now func() time.Time
	// OnPaid 在确认到账、事务提交后调用（用来唤醒商户通知和管理员消息的队列）。
	OnPaid func()
}

func New(db *bun.DB, st *settings.Service) *Service {
	return &Service{db: db, settings: st, Now: time.Now}
}

// Lock 拿住订单核心锁，返回解锁函数。查账对账时用，保证和建单、关单互斥。
func (s *Service) Lock() func() {
	s.mu.Lock()
	return s.mu.Unlock
}

// CreateInput 是建单参数。字段都已经校验过。
type CreateInput struct {
	IsTest     bool
	MerchantID int64
	OutTradeNo string
	Version    int
	Channel    string
	NoTail     bool // 按原价付款，不加尾数（确认型渠道逐单确认，不靠金额推断）
	PayType    string
	Name       string
	Money      string // 原始金额字符串
	Amount     int64  // 分
	NotifyURL  string
	ReturnURL  string
	Param      string
	ClientIP   string
	Device     string
}

// PoolFullError 表示这个金额附近的应付金额全被占满。
type PoolFullError struct {
	RetryAt time.Time
}

func (e *PoolFullError) Error() string {
	return fmt.Sprintf("当前收款繁忙，这个金额暂时无法下单，请在 %s 之后重试", e.RetryAt.In(epay.CST).Format("15:04:05"))
}

// Create 建单。同一商户同一订单号再次下单时按官方规则复用或报错。返回的 bool 表示是不是复用了原订单。
func (s *Service) Create(ctx context.Context, in CreateInput) (*store.Order, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pay, err := s.settings.Payment(ctx)
	if err != nil {
		return nil, false, err
	}
	now := s.Now()
	nowMs := store.Ms(now)

	var old store.Order
	err = s.db.NewSelect().Model(&old).
		Where("merchant_id = ?", in.MerchantID).Where("out_trade_no = ?", in.OutTradeNo).
		Limit(1).Scan(ctx)
	switch {
	case err == nil:
		if old.Status == store.OrderPaid {
			return nil, false, fmt.Errorf("该订单(%s)已完成支付，请勿重复发起支付", in.OutTradeNo)
		}
		if old.IsTest != in.IsTest || old.Version != in.Version || old.Amount != in.Amount || old.Name != in.Name || old.NotifyURL != in.NotifyURL ||
			old.ReturnURL != in.ReturnURL || old.Param != in.Param {
			return nil, false, fmt.Errorf("该订单(%s)支付参数有变化，请更换订单号重新发起支付", in.OutTradeNo)
		}
		if old.Status == store.OrderPending && old.ExpiresAt > nowMs {
			return &old, true, nil
		}
		return nil, false, fmt.Errorf("该订单(%s)已失效，请更换订单号重新发起支付", in.OutTradeNo)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, false, err
	}

	payAmount := in.Amount
	if !in.NoTail {
		if payAmount, err = s.allocate(ctx, in.Channel, in.Amount, pay.MaxTailCents, nowMs); err != nil {
			return nil, false, err
		}
	}
	tradeNo, err := s.newTradeNo(ctx, now)
	if err != nil {
		return nil, false, err
	}
	expires := now.Add(pay.OrderTTL())
	o := &store.Order{
		TradeNo: tradeNo, MerchantID: in.MerchantID, OutTradeNo: in.OutTradeNo, Version: in.Version, IsTest: in.IsTest,
		Channel: in.Channel, PayType: in.PayType, Name: in.Name, Money: in.Money,
		Amount: in.Amount, PayAmount: payAmount,
		NotifyURL: in.NotifyURL, ReturnURL: in.ReturnURL, Param: in.Param,
		ClientIP: in.ClientIP, Device: in.Device, Status: store.OrderPending,
		CreatedAt: nowMs, ExpiresAt: store.Ms(expires),
		ReleasedAt: store.Ms(expires.Add(pay.Cooldown())), UpdatedAt: nowMs,
	}
	if _, err := s.db.NewInsert().Model(o).Exec(ctx); err != nil {
		return nil, false, err
	}
	return o, false, nil
}

// allocate 在本渠道的 amount+1 … amount+maxTail 分里挑最小的空闲应付金额。不同渠道的金额池互不影响。
func (s *Service) allocate(ctx context.Context, channelKey string, amount int64, maxTail int, nowMs int64) (int64, error) {
	var busy []struct {
		PayAmount  int64 `bun:"pay_amount"`
		ReleasedAt int64 `bun:"released_at"`
	}
	err := s.db.NewSelect().Model((*store.Order)(nil)).
		Column("pay_amount", "released_at").
		Where("channel = ?", channelKey).
		Where("pay_amount BETWEEN ? AND ?", amount+1, amount+int64(maxTail)).
		Where("released_at > ?", nowMs).
		Scan(ctx, &busy)
	if err != nil {
		return 0, err
	}
	taken := make(map[int64]bool, len(busy))
	var earliest int64
	for _, b := range busy {
		taken[b.PayAmount] = true
		if earliest == 0 || b.ReleasedAt < earliest {
			earliest = b.ReleasedAt
		}
	}
	for k := int64(1); k <= int64(maxTail); k++ {
		if !taken[amount+k] {
			return amount + k, nil
		}
	}
	return 0, &PoolFullError{RetryAt: store.Time(earliest)}
}

// newTradeNo 生成平台订单号：北京时间 YmdHis + 5 位随机数，共 19 位。
func (s *Service) newTradeNo(ctx context.Context, now time.Time) (string, error) {
	prefix := now.In(epay.CST).Format("20060102150405")
	for range 20 {
		n, err := rand.Int(rand.Reader, big.NewInt(88889))
		if err != nil {
			return "", err
		}
		no := fmt.Sprintf("%s%05d", prefix, n.Int64()+11111)
		exists, err := s.db.NewSelect().Model((*store.Order)(nil)).Where("trade_no = ?", no).Exists(ctx)
		if err != nil {
			return "", err
		}
		if !exists {
			return no, nil
		}
	}
	return "", errors.New("生成订单号失败，请重试")
}

// Find 按平台订单号或商户订单号找商户的订单，平台订单号优先。
func (s *Service) Find(ctx context.Context, merchantID int64, tradeNo, outTradeNo string) (*store.Order, error) {
	q := s.db.NewSelect().Model((*store.Order)(nil)).Where("merchant_id = ?", merchantID).Where("is_test = ?", false)
	if tradeNo != "" {
		q = q.Where("trade_no = ?", tradeNo)
	} else {
		q = q.Where("out_trade_no = ?", outTradeNo)
	}
	var o store.Order
	if err := q.Limit(1).Scan(ctx, &o); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &o, nil
}

// ByTradeNo 按平台订单号找订单。
func (s *Service) ByTradeNo(ctx context.Context, tradeNo string) (*store.Order, error) {
	var o store.Order
	err := s.db.NewSelect().Model(&o).Where("trade_no = ?", tradeNo).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &o, err
}

// ByID 按内部编号找订单。
func (s *Service) ByID(ctx context.Context, id int64) (*store.Order, error) {
	var o store.Order
	err := s.db.NewSelect().Model(&o).Where("id = ?", id).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &o, err
}

// Effective 返回考虑了到期时间的状态：待支付但已经到期的算已过期。
func Effective(o *store.Order, now time.Time) string {
	if o.Status == store.OrderPending && o.ExpiresAt <= store.Ms(now) {
		return store.OrderExpired
	}
	return o.Status
}

// ExternalStatus 是对外的易支付状态：已支付为 1，其他为 0。
func ExternalStatus(o *store.Order) int {
	if o.Status == store.OrderPaid {
		return 1
	}
	return 0
}

var ErrCannotClose = errors.New("该订单状态不支持关闭！")

// ErrNotPending 表示订单已经不是待支付状态。
var ErrNotPending = errors.New("订单已经不是待支付状态")

// ErrChosen 表示订单已经定了支付方式，不能再换：要换只能取消订单，回商户重新下单。
var ErrChosen = errors.New("这笔订单已经选了支付方式，不能再换")

// Choose 给付款人还没选支付方式的订单（渠道为空）定下渠道和方式。noTail 为假时（按金额推断到账的渠道）
// 当场在这个渠道分配加了尾数的应付金额。已经定成同样的渠道和方式时直接返回订单，定成别的时返回 ErrChosen。
func (s *Service) Choose(ctx context.Context, id int64, channelKey, typ string, noTail bool) (*store.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, err := s.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	nowMs := store.Ms(now)
	switch {
	case Effective(o, now) != store.OrderPending:
		return nil, ErrNotPending
	case o.Channel == channelKey && o.PayType == typ:
		return o, nil
	case o.Channel != "":
		return nil, ErrChosen
	}
	payAmount := o.Amount
	if !noTail {
		pay, err := s.settings.Payment(ctx)
		if err != nil {
			return nil, err
		}
		if payAmount, err = s.allocate(ctx, channelKey, o.Amount, pay.MaxTailCents, nowMs); err != nil {
			return nil, err
		}
	}
	res, err := s.db.NewUpdate().Model((*store.Order)(nil)).
		Set("channel = ?", channelKey).Set("pay_type = ?", typ).Set("pay_amount = ?", payAmount).Set("updated_at = ?", nowMs).
		Where("id = ?", id).Where("status = ?", store.OrderPending).Where("channel = ''").Exec(ctx)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotPending
	}
	o.Channel, o.PayType, o.PayAmount, o.UpdatedAt = channelKey, typ, payAmount, nowMs
	return o, nil
}

// Unchoose 撤回 Choose 定下的渠道和支付方式，让付款人重新选：只用在选定后向第三方平台预下单失败、
// 付款人还没拿到付款码的时候。订单已经换了别的方式、有了预下单记录或者不再是待支付时什么也不做。
func (s *Service) Unchoose(ctx context.Context, id int64, channelKey, typ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.NewUpdate().Model((*store.Order)(nil)).
		Set("channel = ''").Set("pay_type = ''").Set("pay_amount = amount").Set("updated_at = ?", store.Ms(s.Now())).
		Where("id = ?", id).Where("status = ?", store.OrderPending).Where("channel = ?", channelKey).Where("pay_type = ?", typ).
		Where("NOT EXISTS (SELECT 1 FROM upstream_payments WHERE order_id = ?)", id).Exec(ctx)
	return err
}

// Close 关闭未支付的订单。已过期或已关闭的订单再关一次也算成功。
func (s *Service) Close(ctx context.Context, id int64) (*store.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, err := s.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	switch Effective(o, now) {
	case store.OrderPending:
		pay, err := s.settings.Payment(ctx)
		if err != nil {
			return nil, err
		}
		nowMs := store.Ms(now)
		o.Status, o.EndedAt, o.UpdatedAt = store.OrderClosed, nowMs, nowMs
		o.ReleasedAt = min(o.ReleasedAt, store.Ms(now.Add(pay.Cooldown())))
		_, err = s.db.NewUpdate().Model(o).Column("status", "ended_at", "released_at", "updated_at").
			WherePK().Where("status = ?", store.OrderPending).Exec(ctx)
		return o, err
	case store.OrderExpired, store.OrderClosed:
		return o, nil
	default:
		return nil, ErrCannotClose
	}
}

// ExpireDue 把到期的待支付订单标成已过期。结束时刻记原定到期时刻，冷却从那时算。
func (s *Service) ExpireDue(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	nowMs := store.Ms(s.Now())
	res, err := s.db.NewUpdate().Model((*store.Order)(nil)).
		Set("status = ?", store.OrderExpired).
		Set("ended_at = expires_at").
		Set("updated_at = ?", nowMs).
		Where("status = ?", store.OrderPending).
		Where("expires_at <= ?", nowMs).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkPaid 开一个事务把订单标成已支付，关联流水，排入商户通知和给管理员的到账消息；then 不为空时在同一个事务里接着执行。
// 调用方要先拿住 Lock。事务里只能用 tx，不能再碰 s.db 或读设置（设置在开事务前读好）。
func (s *Service) MarkPaid(ctx context.Context, o *store.Order, t *store.Transaction, then func(ctx context.Context, tx bun.Tx) error) error {
	return s.MarkPaidTx(ctx, o, t, nil, then)
}

// MarkPaidTx 和 MarkPaid 一样，只是在标记之前先在同一个事务里执行 before（例如插入流水）。
func (s *Service) MarkPaidTx(ctx context.Context, o *store.Order, t *store.Transaction, before, then func(ctx context.Context, tx bun.Tx) error) error {
	pay, err := s.settings.Payment(ctx)
	if err != nil {
		return err
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if before != nil {
			if err := before(ctx, tx); err != nil {
				return err
			}
		}
		if err := s.markPaid(ctx, tx, o, t, pay); err != nil {
			return err
		}
		if then != nil {
			return then(ctx, tx)
		}
		return nil
	})
}

func (s *Service) markPaid(ctx context.Context, db bun.IDB, o *store.Order, t *store.Transaction, pay settings.Payment) error {
	nowMs := store.Ms(s.Now())
	prev := o.Status
	o.Status = store.OrderPaid
	o.PaidAt = t.OccurredAt
	o.PaidAmount = t.Amount
	o.TxnID = t.ID
	o.EndedAt = t.OccurredAt
	o.APITradeNo = t.AlipayOrderNo
	o.Buyer = t.OtherAccount
	o.UpdatedAt = nowMs
	if prev == store.OrderPending {
		o.ReleasedAt = min(o.ReleasedAt, t.OccurredAt+int64(pay.CooldownSeconds)*1000)
	}
	res, err := db.NewUpdate().Model(o).
		Column("status", "paid_at", "paid_amount", "txn_id", "ended_at", "api_trade_no", "buyer", "updated_at", "released_at").
		WherePK().Where("status = ?", prev).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("订单 %s 状态已变化", o.TradeNo)
	}
	t.State, t.OrderID, t.UpdatedAt = store.TxnMatched, o.ID, nowMs
	if _, err := db.NewUpdate().Model(t).Column("state", "order_id", "updated_at").WherePK().Exec(ctx); err != nil {
		return err
	}
	n := &store.Notification{
		OrderID: o.ID, Status: store.NotifyPending, NextAt: nowMs, CreatedAt: nowMs, UpdatedAt: nowMs,
	}
	_, err = db.NewInsert().Model(n).
		On("CONFLICT (order_id) DO UPDATE").
		Set("status = EXCLUDED.status").Set("attempts = 0").Set("first_at = 0").
		Set("next_at = EXCLUDED.next_at").Set("last_error = ''").Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return err
	}
	return store.QueueAdminEvent(ctx, db, store.AdminEventPaid, o.ID)
}

// Stats 是一个商户的订单统计，给易支付的商户信息接口用。
type Stats struct {
	Total, PaidToday, PaidYesterday int64
	AmountToday, AmountYesterday    int64
}

// MerchantStats 统计商户的订单数和今日、昨日的已付笔数与金额（按北京时间分日）。
func (s *Service) MerchantStats(ctx context.Context, merchantID int64) (Stats, error) {
	var st Stats
	total, err := s.db.NewSelect().Model((*store.Order)(nil)).Where("merchant_id = ?", merchantID).Where("is_test = ?", false).Count(ctx)
	if err != nil {
		return st, err
	}
	st.Total = total
	now := s.Now().In(epay.CST)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, epay.CST)
	yesterday := today.AddDate(0, 0, -1)
	sum := func(from, to time.Time) (int64, int64, error) {
		var r struct {
			N   int64         `bun:"n"`
			Sum sql.NullInt64 `bun:"s"`
		}
		err := s.db.NewSelect().Model((*store.Order)(nil)).
			ColumnExpr("COUNT(*) AS n").ColumnExpr("SUM(amount) AS s").
			Where("merchant_id = ?", merchantID).Where("is_test = ?", false).Where("status = ?", store.OrderPaid).
			Where("paid_at >= ?", store.Ms(from)).Where("paid_at < ?", store.Ms(to)).
			Scan(ctx, &r)
		return r.N, r.Sum.Int64, err
	}
	if st.PaidToday, st.AmountToday, err = sum(today, today.AddDate(0, 0, 1)); err != nil {
		return st, err
	}
	st.PaidYesterday, st.AmountYesterday, err = sum(yesterday, today)
	return st, err
}

// ListForMerchant 按平台订单号倒序列出商户的订单。status 为 nil 时不筛选；0 表示未支付，1 表示已支付。
func (s *Service) ListForMerchant(ctx context.Context, merchantID int64, status *int, limit, offset int) ([]store.Order, error) {
	q := s.db.NewSelect().Model((*store.Order)(nil)).Where("merchant_id = ?", merchantID).Where("is_test = ?", false)
	if status != nil {
		switch *status {
		case 1:
			q = q.Where("status = ?", store.OrderPaid)
		case 0:
			q = q.Where("status <> ?", store.OrderPaid)
		default:
			return nil, nil
		}
	}
	var os []store.Order
	err := q.Order("trade_no DESC").Limit(int64(limit)).Offset(int64(offset)).Scan(ctx, &os)
	return os, err
}
