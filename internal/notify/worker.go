package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"
	"golang.org/x/sync/errgroup"

	"tillbox/internal/store"
)

// Schedule 是每次投递相对首次投递的时间点。前 5 次和官方相同，之后追加 2、6、24 小时三次，然后进死信。
var Schedule = []time.Duration{
	0, time.Minute, 3 * time.Minute, 19 * time.Minute, 55 * time.Minute,
	2 * time.Hour, 6 * time.Hour, 24 * time.Hour,
}

// minGap 是错过时间点（例如停机后）补发时，两次投递之间的最短间隔。
const minGap = 30 * time.Second

type Worker struct {
	db     *bun.DB
	signer *Signer
	client *http.Client
	wake   chan struct{}
	// Now 可以在测试里替换。
	Now func() time.Time

	mu       sync.Mutex
	inflight map[int64]bool
}

func NewWorker(db *bun.DB, signer *Signer) *Worker {
	return &Worker{
		db:     db,
		signer: signer,
		client: &http.Client{
			Timeout: 5 * time.Second,
			// 和官方一样不跟随重定向，只看第一次响应的正文。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		wake:     make(chan struct{}, 1),
		Now:      time.Now,
		inflight: map[int64]bool{},
	}
}

// SetClient 替换 HTTP 客户端（测试用）。
func (w *Worker) SetClient(c *http.Client) { w.client = c }

// Wake 让队列马上看一眼有没有要发的通知。
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run 持续投递到期的通知，直到 ctx 结束。
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		if err := w.RunDue(ctx); err != nil && ctx.Err() == nil {
			slog.Error("通知队列出错", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.wake:
		}
	}
}

// RunDue 投递所有到期的通知。
func (w *Worker) RunDue(ctx context.Context) error {
	var due []store.Notification
	err := w.db.NewSelect().Model(&due).
		Where("status = ?", store.NotifyPending).
		Where("next_at <= ?", store.Ms(w.Now())).
		Order("next_at ASC").Limit(100).Scan(ctx)
	if err != nil {
		return err
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for _, n := range due {
		g.Go(func() error {
			if _, err := w.attempt(ctx, n.OrderID, false); err != nil {
				slog.Warn("通知投递出错", "order_id", n.OrderID, "err", err)
			}
			return nil
		})
	}
	return g.Wait()
}

// Resend 手动重发一次。不改变自动重试的时间点；送达后队列状态变成成功。
func (w *Worker) Resend(ctx context.Context, orderID int64) (*store.NotifyAttempt, error) {
	return w.attempt(ctx, orderID, true)
}

var ErrBusy = errors.New("这笔通知正在发送，请稍后再试")

func (w *Worker) attempt(ctx context.Context, orderID int64, manual bool) (*store.NotifyAttempt, error) {
	w.mu.Lock()
	if w.inflight[orderID] {
		w.mu.Unlock()
		return nil, ErrBusy
	}
	w.inflight[orderID] = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.inflight, orderID)
		w.mu.Unlock()
	}()

	var n store.Notification
	if err := w.db.NewSelect().Model(&n).Where("order_id = ?", orderID).Scan(ctx); err != nil {
		return nil, err
	}
	var o store.Order
	if err := w.db.NewSelect().Model(&o).Where("id = ?", orderID).Scan(ctx); err != nil {
		return nil, err
	}
	now := w.Now()
	nowMs := store.Ms(now)
	if o.Status != store.OrderPaid {
		n.Status, n.LastError, n.UpdatedAt = store.NotifyDead, "订单已不是已支付状态，停止通知", nowMs
		_, err := w.db.NewUpdate().Model(&n).Column("status", "last_error", "updated_at").WherePK().Exec(ctx)
		if err != nil {
			return nil, err
		}
		return nil, errors.New(n.LastError)
	}
	if !manual && (n.Status != store.NotifyPending || n.NextAt > nowMs) {
		return nil, nil // 别处已经处理过了
	}

	params, err := w.signer.Params(ctx, &o, now)
	if err != nil {
		return nil, err
	}
	target := AppendQuery(o.NotifyURL, params)
	a := w.send(ctx, target)
	a.OrderID, a.Manual, a.CreatedAt = orderID, manual, nowMs
	if _, err := w.db.NewInsert().Model(a).Exec(ctx); err != nil {
		return nil, err
	}

	// 重新读一遍，避免和并发的手动重发互相覆盖。
	if err := w.db.NewSelect().Model(&n).Where("order_id = ?", orderID).Scan(ctx); err != nil {
		return nil, err
	}
	n.LastAt, n.UpdatedAt = nowMs, nowMs
	if a.Success {
		n.Status, n.LastError = store.NotifySuccess, ""
	} else {
		n.LastError = a.Error
		if n.LastError == "" {
			n.LastError = fmt.Sprintf("HTTP %d：%s", a.HTTPStatus, truncate(a.Response, 200))
		}
	}
	if !manual {
		n.Attempts++
		if n.FirstAt == 0 {
			n.FirstAt = nowMs
		}
		if !a.Success {
			if n.Attempts >= len(Schedule) {
				n.Status = store.NotifyDead
			} else {
				next := store.Time(n.FirstAt).Add(Schedule[n.Attempts])
				n.NextAt = store.Ms(maxTime(next, now.Add(minGap)))
			}
		}
	}
	_, err = w.db.NewUpdate().Model(&n).
		Column("status", "attempts", "first_at", "next_at", "last_at", "last_error", "updated_at").
		WherePK().Exec(ctx)
	return a, err
}

func (w *Worker) send(ctx context.Context, target string) *store.NotifyAttempt {
	a := &store.NotifyAttempt{URL: target}
	start := time.Now()
	defer func() { a.DurationMs = time.Since(start).Milliseconds() }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		a.Error = "通知地址无效：" + err.Error()
		return a
	}
	req.Header.Set("User-Agent", "Tillbox-Notify/1.0")
	req.Header.Set("Accept", "*/*")
	resp, err := w.client.Do(req)
	if err != nil {
		a.Error = err.Error()
		return a
	}
	defer resp.Body.Close()
	a.HTTPStatus = resp.StatusCode
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		a.Error = "读取响应失败：" + err.Error()
	}
	text := string(body)
	a.Response = truncate(text, 1000)
	// 官方只看正文里有没有 success，不看状态码。
	a.Success = strings.Contains(text, "success") || strings.Contains(text, "SUCCESS") || strings.Contains(text, "Success")
	if a.Success {
		a.Error = ""
	}
	return a
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
