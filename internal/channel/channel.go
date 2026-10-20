// Package channel 定义收款渠道。
//
// 渠道是后台渠道表里的一条记录，按确认方式分两种：
//   - 推断型（支付宝经营码、演示渠道）：渠道只负责给付款指引和拉流水，
//     金额池和"哪笔流水属于哪个订单"的推断放在核心里公用，按渠道实例隔离；
//   - 确认型（上游易支付）：渠道自己预下单、收回调、查单，逐单确认。
package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"tillbox/internal/epay"
	"tillbox/internal/store"
)

// TypeNames 是易支付 type 的中文名。
var TypeNames = map[string]string{
	"alipay": "支付宝",
	"wxpay":  "微信支付",
	"qqpay":  "QQ钱包",
	"bank":   "云闪付",
	"jdpay":  "京东支付",
	"paypal": "PayPal",
	"usdt":   "USDT",
}

// TypeLabel 返回支付方式的中文名，不认识的原样返回。
func TypeLabel(t string) string {
	if n := TypeNames[t]; n != "" {
		return n
	}
	return t
}

// TypeLabels 把多个支付方式拼成"支付宝或微信支付"。
func TypeLabels(ts []string) string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = TypeLabel(t)
	}
	return strings.Join(out, "或")
}

// Guide 是收银台给付款人看的收款指引。
type Guide struct {
	// QRCode 是要画成二维码的内容，例如经营码链接。
	QRCode string `json:"qrcode"`
	// PayeeName 是收款方名称，可空。
	PayeeName string `json:"payee_name"`
	// App 是让付款人用哪个 App 扫码，例如"支付宝"。
	App string `json:"app"`
	// ManualAmount 为真时，付款人要自己输入金额（静态码）。
	ManualAmount bool `json:"manual_amount"`
	// PayURL 是“去付款”按钮的链接（上游给的付款页或唤起 App 的链接）。
	PayURL string `json:"pay_url,omitempty"`
	// Submit 为真时，“去付款”要先向收银台要一个现签的跳转地址（上游只给 HTML 表单）。
	Submit bool `json:"submit,omitempty"`
}

// Channel 是所有渠道的公共部分。
type Channel interface {
	// Key 是渠道实例键 ch<编号>，存进订单和流水。
	Key() string
	// PayTypes 是开放的易支付 type，例如 alipay；第一个是调用方没传 type 时的默认值。
	PayTypes() []string
	// Guide 返回某个订单的收款指引。
	Guide(ctx context.Context, o *store.Order) (Guide, error)
	// Ready 检查渠道能不能收款，不能时返回原因（给后台和日志看）。
	Ready(ctx context.Context) error
}

// Instance 是渠道注册表管理的实例：后台改了渠道配置后原地更新，查账进度和内存状态都保留。
type Instance interface {
	Channel
	// Configure 应用渠道表里的最新一行。配置有错时不返回错误，由 Ready 报告。
	Configure(row store.Channel)
}

// Base 是各渠道实例共用的渠道表信息。
type Base struct {
	mu  sync.RWMutex
	row store.Channel
}

// SetRow 换成渠道表里的最新一行。
func (b *Base) SetRow(row store.Channel) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.row = row
}

// Row 返回渠道表里的这一行（Config 是原文，密钥仍是加密的）。
func (b *Base) Row() store.Channel {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.row
}

func (b *Base) Key() string { return b.Row().Key() }

// CheckEnabled 在渠道停用时返回错误。
func (b *Base) CheckEnabled() error {
	if r := b.Row(); !r.Enabled {
		return fmt.Errorf("渠道「%s」已停用", r.Name)
	}
	return nil
}

// Entry 是渠道返回的一条账务流水。
type Entry struct {
	ExternalID      string
	Direction       string // store.DirIn / store.DirOut
	Amount          int64  // 分，取绝对值
	OccurredAt      time.Time
	AlipayOrderNo   string
	MerchantOrderNo string
	OtherAccount    string
	Memo            string
	BizDesc         string
	Kind            string
	Raw             string // 渠道返回的原文（JSON）
	// Invalid 是这条流水解析不了的原因。非空时其他字段只填读得出的部分，
	// 入库后记异常等人工核对，不参与匹配；一条坏流水不影响同一批的其他流水。
	Invalid string
}

// LedgerSource 由推断型渠道实现：按时间窗口拉流水。窗口左闭右开。
type LedgerSource interface {
	Channel
	Fetch(ctx context.Context, start, end time.Time) ([]Entry, error)
	// MaxSpan 是单次查询允许的最大时间跨度。
	MaxSpan() time.Duration
}

// Payment 是向上游预下单的结果：上游订单号和给付款人的付款信息。
type Payment struct {
	TradeNo  string
	InfoType string // store.InfoQRCode、InfoJump、InfoURLScheme、InfoSubmit
	Info     string
}

// Paid 是上游确认的一笔付款（来自回调或查单）。
type Paid struct {
	TradeNo    string // 上游订单号，作为流水号入库
	OutTradeNo string // 本系统的平台订单号
	Type       string
	Amount     int64  // 分
	APITradeNo string // 支付宝、微信等的交易号
	Buyer      string
	PaidAt     time.Time // 上游给出的付款时间，零值表示不知道
	Raw        string    // 上游给的原文（JSON），不含签名
}

// Confirmer 由确认型渠道（上游易支付）实现：预下单、验回调、查单，逐单确认到账。
// 回调和查单不看渠道是否启用：停用只是不再接新订单，已经付的款照样要确认。
type Confirmer interface {
	Channel
	// Prepay 按订单的支付方式向上游预下单。base 是本系统的对外地址，用来拼回调和回跳地址。
	Prepay(ctx context.Context, o *store.Order, base string) (Payment, error)
	// SubmitURL 生成跳转到上游付款页的地址（上游只给 HTML 表单时用），每次现签。
	SubmitURL(ctx context.Context, o *store.Order, base string) (string, error)
	// Query 向上游查单，没付款时返回 nil。tradeNo 是上游订单号，空时按平台订单号查。
	Query(ctx context.Context, o *store.Order, tradeNo string) (*Paid, error)
	// VerifyNotify 校验上游的回调参数，返回确认的付款。
	VerifyNotify(p map[string]string) (*Paid, error)
}

// Limiter 由有单笔金额限制的渠道实现（上游易支付）。
type Limiter interface {
	// CheckAmount 检查金额（分）在不在支付方式的单笔限额内，超出时返回 *AmountError。
	CheckAmount(typ string, amount int64, now time.Time) error
}

// AmountError 是金额超出了支付方式的单笔限额。Error 给日志和后台看，被上游拒绝时是上游的原话；
// Message 给付款人和调用方看。
type AmountError struct {
	Type     string
	Min, Max int64 // 分，超出的那一边非 0
	// Err 是上游拒单的原错误；按已知限额提前拦下时为空。
	Err error
}

// Rule 是超出的那条限额，例如“单笔最低 0.30 元”。
func (e *AmountError) Rule() string {
	if e.Min > 0 {
		return "单笔最低 " + epay.FormatCents(e.Min) + " 元"
	}
	return "单笔最高 " + epay.FormatCents(e.Max) + " 元"
}

func (e *AmountError) Message() string { return TypeLabel(e.Type) + e.Rule() }

func (e *AmountError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message()
}

func (e *AmountError) Unwrap() error { return e.Err }

// ErrAuth 表示凭据或权限错误。查账遇到它至少停 15 分钟。
var ErrAuth = errors.New("渠道凭据或权限错误")

// ErrNotConfigured 表示渠道还没配置好。
var ErrNotConfigured = errors.New("收款渠道还没有配置")

// RetryAfterError 表示渠道要求等一段时间再试（例如限流）。
type RetryAfterError struct {
	After time.Duration
	Err   error
}

func (e *RetryAfterError) Error() string {
	return fmt.Sprintf("%v（%s 后重试）", e.Err, e.After)
}

func (e *RetryAfterError) Unwrap() error { return e.Err }

// InvalidError 是后台提交的渠道配置不合格，内容直接给管理员看。
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

// Invalid 返回一个配置不合格的错误。
func Invalid(format string, args ...any) error {
	return &InvalidError{Msg: fmt.Sprintf(format, args...)}
}

// Tester 由能测试连接的渠道实现（后台“测试连接”）。
type Tester interface {
	Test(ctx context.Context) error
}
