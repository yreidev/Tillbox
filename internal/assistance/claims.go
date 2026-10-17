package assistance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/ledger"
	"tillbox/internal/store"
)

var referencePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,80}$`)

// Payer claims allowed within 24 hours.
const (
	maxClaimsPerReference = 5
	maxClaimsPerOrder     = 10
)

var ErrRateLimit = errors.New("请求过于频繁，请稍后再试")
var ErrInvalidClaim = errors.New("请填写有效的支付宝交易号、实付金额和付款时间；说明不超过 1000 字，联系方式不超过 100 字")

type ClaimInput struct {
	Kind       string `json:"kind"`
	PaymentRef string `json:"payment_ref"`
	Amount     int64  `json:"amount_cents"`
	PaidAt     int64  `json:"paid_at"`
	Message    string `json:"message"`
	Contact    string `json:"contact"`
}

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Submit returns the only copy of a random receipt. Sequential order numbers
// can never retrieve someone else's support ticket or contact information.
func (s *Service) Submit(ctx context.Context, orderID int64, in ClaimInput) (string, *store.AssistanceCase, error) {
	in.PaymentRef = strings.TrimSpace(in.PaymentRef)
	in.Message = strings.TrimSpace(in.Message)
	in.Contact = strings.TrimSpace(in.Contact)
	if !referencePattern.MatchString(in.PaymentRef) || in.Amount <= 0 || in.Amount > 100_000_000 || in.PaidAt <= 0 || in.PaidAt > store.Now()+5*time.Minute.Milliseconds() || in.PaidAt < store.Now()-30*24*time.Hour.Milliseconds() || len([]rune(in.Message)) > 1000 || len([]rune(in.Contact)) > 100 {
		return "", nil, ErrInvalidClaim
	}
	switch in.Kind {
	case "not_received", "wrong_amount", "late", "duplicate", "other":
	default:
		return "", nil, ErrInvalidClaim
	}
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	recent := func() *bun.SelectQuery {
		return s.DB.NewSelect().Model((*store.AssistanceCase)(nil)).Where("source = ?", "payer").Where("created_at > ?", store.Now()-24*time.Hour.Milliseconds())
	}
	// Quotas are per payment reference, never shared by all unbound claims:
	// a shared order_id=0 quota would let one payer block every other payer.
	count, err := recent().Where("payment_ref = ?", in.PaymentRef).Count(ctx)
	if err != nil {
		return "", nil, err
	}
	if count >= maxClaimsPerReference {
		return "", nil, ErrRateLimit
	}
	if orderID > 0 {
		// Anyone who sees the order number could use up an order quota, so a
		// reference proven by a real incoming transaction is exempt from it.
		if count, err = recent().Where("order_id = ?", orderID).Count(ctx); err != nil {
			return "", nil, err
		}
		if count >= maxClaimsPerOrder {
			proven, err := s.DB.NewSelect().Model((*store.Transaction)(nil)).
				Where("direction = ?", store.DirIn).Where("amount = ?", in.Amount).
				WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
					return q.Where("alipay_order_no = ?", in.PaymentRef).WhereOr("external_id = ?", in.PaymentRef)
				}).Exists(ctx)
			if err != nil {
				return "", nil, err
			}
			if !proven {
				return "", nil, ErrRateLimit
			}
		}
	}
	var raw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(raw[:])
	c := &store.AssistanceCase{DedupeKey: "payer:" + TokenHash(token), OrderID: orderID, Source: "payer", Kind: in.Kind, PaymentRef: in.PaymentRef, ClaimAmount: in.Amount, PaidAt: in.PaidAt, Message: in.Message, Contact: in.Contact, TokenHash: TokenHash(token)}
	if err = s.add(ctx, c); err != nil {
		return "", nil, err
	}
	if orderID == 0 {
		if err = s.findClaimOrder(ctx, c); err != nil {
			return "", nil, err
		}
	}
	s.Wake()
	if s.OnClaim != nil {
		s.OnClaim()
	}
	return token, c, nil
}

// findClaimOrder only associates a support case. It never marks a payment as
// paid: any recovery still passes through Ledger.LinkVerified. The claim lock
// must be held by the caller; the order lock stabilizes ledger ownership.
func (s *Service) findClaimOrder(ctx context.Context, c *store.AssistanceCase) error {
	// 在所有经营码（推断型）渠道的流水里找；同一凭证命中多条时不算唯一。
	keys := s.Pollers.Keys()
	unlock := s.Orders.Lock()
	defer unlock()
	var ts []store.Transaction
	if len(keys) > 0 {
		err := s.DB.NewSelect().Model(&ts).Where("channel IN (?)", bun.List(keys)).
			WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
				return q.Where("alipay_order_no = ?", c.PaymentRef).WhereOr("external_id = ?", c.PaymentRef)
			}).Limit(2).Scan(ctx)
		if err != nil {
			return err
		}
	}
	var orderID, txnID int64
	if len(ts) == 1 {
		t := ts[0]
		if t.Direction == store.DirIn && t.Amount == c.ClaimAmount && c.PaidAt >= t.OccurredAt-2*time.Minute.Milliseconds() && c.PaidAt <= t.OccurredAt+2*time.Minute.Milliseconds() {
			blocked, e := s.DB.NewSelect().Model((*store.Anomaly)(nil)).Where("transaction_id = ?", t.ID).
				Where("kind IN (?)", bun.List([]string{store.AnomalyConflict, store.AnomalyAmbiguous, store.AnomalyDuplicate, store.AnomalyUnlinked})).Exists(ctx)
			if e != nil {
				return e
			}
			if !blocked && t.OrderID > 0 && t.State == store.TxnMatched {
				o, e := s.Orders.ByID(ctx, t.OrderID)
				if e != nil {
					return e
				}
				if o.Channel == t.Channel && o.Status == store.OrderPaid && o.TxnID == t.ID && o.PaidAmount == t.Amount {
					orderID, txnID = o.ID, t.ID
				}
			} else if !blocked && t.OrderID == 0 && (t.State == store.TxnNew || t.State == store.TxnAnomaly) {
				var candidates []store.Order
				if e := s.DB.NewSelect().Model(&candidates).Where("channel = ?", t.Channel).Where("pay_amount = ?", t.Amount).
					Where("created_at < ?", t.OccurredAt+ledger.TimePrecision.Milliseconds()).Where("expires_at >= ?", t.OccurredAt-ledger.LateWindow.Milliseconds()).Limit(2).Scan(ctx); e != nil {
					return e
				}
				if len(candidates) == 1 && (candidates[0].Status == store.OrderPending || candidates[0].Status == store.OrderExpired) {
					orderID, txnID = candidates[0].ID, t.ID
				}
			}
		}
	}
	if orderID > 0 {
		conflict, e := s.DB.NewSelect().Model((*store.AssistanceCase)(nil)).Where("source = ?", "payer").Where("payment_ref = ?", c.PaymentRef).Where("order_id > 0").Where("order_id <> ?", orderID).Exists(ctx)
		if e != nil {
			return e
		}
		if conflict {
			orderID, txnID = 0, 0
		}
	}
	c.OrderID, c.TransactionID = orderID, txnID
	if orderID == 0 {
		return s.finish(ctx, c, "manual", "无平台订单号，付款证据尚未确认唯一归属", "已收到付款凭证，需人工核对订单归属。请保留支付宝账单。", "find_order", "没有向付款人披露候选订单，未修改账务")
	}
	return s.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model(c).Column("order_id", "transaction_id").WherePK().Exec(ctx); err != nil {
			return err
		}
		_, err := tx.NewInsert().Model(&store.AssistanceAction{CaseID: c.ID, Action: "find_order", Reason: "交易号、金额及时间与唯一真实流水一致", Result: "已关联申诉订单；未修改账务状态", CreatedAt: store.Now()}).Exec(ctx)
		return err
	})
}

// Limiter bounds per-IP public traffic; only hashes of addresses are retained.
type Limiter struct {
	mu      sync.Mutex
	entries map[string][]time.Time
}

func (l *Limiter) Allow(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.entries == nil {
		l.entries = map[string][]time.Time{}
	}
	if len(l.entries) > 1000 {
		for k, v := range l.entries {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > 15*time.Minute {
				delete(l.entries, k)
			}
		}
	}
	var active []time.Time
	key = TokenHash(key)
	for _, at := range l.entries[key] {
		if now.Sub(at) < 15*time.Minute {
			active = append(active, at)
		}
	}
	if len(active) >= max {
		return false
	}
	if len(l.entries) >= 10_000 && len(active) == 0 {
		return false
	}
	l.entries[key] = append(active, now)
	return true
}
