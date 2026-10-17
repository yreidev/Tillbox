package store

import (
	"strconv"
	"strings"

	"github.com/uptrace/bun"
)

type TestReceipt struct {
	bun.BaseModel `bun:"table:test_receipts,alias:receipt"`
	OrderID       int64 `bun:"order_id,pk" json:"order_id"`
	Deliveries    int64 `bun:"deliveries" json:"deliveries"`
	FirstAt       int64 `bun:"first_at" json:"first_at"`
	LastAt        int64 `bun:"last_at" json:"last_at"`
}

type ArchiveBatch struct {
	bun.BaseModel `bun:"table:archive_batches"`
	ID            int64  `bun:"id,pk,autoincrement" json:"id"`
	Filename      string `bun:"filename" json:"filename"`
	SHA256        string `bun:"sha256" json:"sha256"`
	Records       int64  `bun:"records" json:"records"`
	CreatedAt     int64  `bun:"created_at" json:"created_at"`
}

// Admin 是后台管理员。个人自用，只有一个管理员，登录只要密码。
type Admin struct {
	bun.BaseModel `bun:"table:admins"`

	ID           int64  `bun:"id,pk,autoincrement" json:"id"`
	PasswordHash string `bun:"password_hash" json:"-"`
	CreatedAt    int64  `bun:"created_at" json:"created_at"`
	UpdatedAt    int64  `bun:"updated_at" json:"updated_at"`
}

type Session struct {
	bun.BaseModel `bun:"table:sessions"`

	TokenHash string `bun:"token_hash,pk" json:"-"`
	AdminID   int64  `bun:"admin_id" json:"admin_id"`
	IP        string `bun:"ip" json:"ip"`
	UserAgent string `bun:"user_agent" json:"user_agent"`
	CreatedAt int64  `bun:"created_at" json:"created_at"`
	ExpiresAt int64  `bun:"expires_at" json:"expires_at"`
}

type Setting struct {
	bun.BaseModel `bun:"table:settings"`

	Key       string `bun:"key,pk" json:"key"`
	Value     string `bun:"value" json:"value"`
	UpdatedAt int64  `bun:"updated_at" json:"updated_at"`
}

// 商户的密钥类型
const (
	KeyTypeAny     = 0 // MD5 和 RSA 都收
	KeyTypeRSAOnly = 1 // 只收 RSA
)

type Merchant struct {
	bun.BaseModel `bun:"table:merchants"`

	ID           int64  `bun:"id,pk" json:"id"`
	Name         string `bun:"name" json:"name"`
	MD5Key       string `bun:"md5_key" json:"-"` // 主密钥加密
	RSAPublicKey string `bun:"rsa_public_key" json:"rsa_public_key"`
	KeyType      int    `bun:"key_type" json:"key_type"`
	Domains      string `bun:"domains" json:"domains"` // 通知和回跳域名白名单，一行一个；空表示不限制
	Enabled      bool   `bun:"enabled" json:"enabled"`
	ChannelID    int64  `bun:"channel_id" json:"channel_id"` // 收款用的渠道；0 表示还没选
	Remark       string `bun:"remark" json:"remark"`
	CreatedAt    int64  `bun:"created_at" json:"created_at"`
	UpdatedAt    int64  `bun:"updated_at" json:"updated_at"`
}

// 渠道类型
const (
	ChannelAlipayQR = "alipay_qr" // 支付宝经营码：查账加金额尾数推断
	ChannelEpay     = "epay"      // 上游易支付：预下单、回调、查单逐单确认
	ChannelDemo     = "demo"      // 演示渠道：流水在内存里，只在 TILLBOX_CHANNEL=fake 时可用
)

// Channel 是一个支付渠道实例。
type Channel struct {
	bun.BaseModel `bun:"table:channels,alias:ch"`

	ID        int64  `bun:"id,pk,autoincrement" json:"id"`
	Type      string `bun:"type" json:"type"`
	Name      string `bun:"name" json:"name"`
	Enabled   bool   `bun:"enabled" json:"enabled"`
	Config    string `bun:"config" json:"-"` // JSON，密钥字段用主密钥加密
	Remark    string `bun:"remark" json:"remark"`
	CreatedAt int64  `bun:"created_at" json:"created_at"`
	UpdatedAt int64  `bun:"updated_at" json:"updated_at"`
}

// Key 是渠道实例键，存进订单和流水的 channel 列。
func (c Channel) Key() string { return ChannelKey(c.ID) }

// ChannelKey 返回编号对应的渠道实例键 ch<编号>。
func ChannelKey(id int64) string { return "ch" + strconv.FormatInt(id, 10) }

// ChannelID 从渠道实例键解析编号，格式不对时返回 0。
func ChannelID(key string) int64 {
	s, ok := strings.CutPrefix(key, "ch")
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// 上游付款信息类型
const (
	InfoQRCode    = "qrcode"    // 在收银台画成二维码
	InfoJump      = "jump"      // 跳转到上游付款页
	InfoURLScheme = "urlscheme" // 唤起 App 的链接
	InfoSubmit    = "submit"    // 上游只给 HTML 表单：付款人点“去付款”时现签一个跳转地址
)

// UpstreamPayment 是向上游易支付预下单的结果。
type UpstreamPayment struct {
	bun.BaseModel `bun:"table:upstream_payments,alias:up"`

	OrderID    int64  `bun:"order_id,pk" json:"order_id"`
	ChannelID  int64  `bun:"channel_id" json:"channel_id"`
	PayType    string `bun:"pay_type" json:"pay_type"`
	TradeNo    string `bun:"trade_no" json:"trade_no"` // 上游订单号
	InfoType   string `bun:"info_type" json:"info_type"`
	Info       string `bun:"info" json:"info"`
	CheckedAt  int64  `bun:"checked_at" json:"checked_at"` // 上次查单的时刻
	CheckError string `bun:"check_error" json:"check_error"`
	CreatedAt  int64  `bun:"created_at" json:"created_at"`
	UpdatedAt  int64  `bun:"updated_at" json:"updated_at"`
}

// 订单状态
const (
	OrderPending  = "pending"  // 待支付
	OrderPaid     = "paid"     // 已支付
	OrderExpired  = "expired"  // 已过期
	OrderClosed   = "closed"   // 已关闭
	OrderDisputed = "disputed" // 撤销了到账关联，待人工处理
)

type Order struct {
	bun.BaseModel `bun:"table:orders,alias:o"`

	ID         int64  `bun:"id,pk,autoincrement" json:"id"`
	TradeNo    string `bun:"trade_no" json:"trade_no"`
	MerchantID int64  `bun:"merchant_id" json:"merchant_id"`
	OutTradeNo string `bun:"out_trade_no" json:"out_trade_no"`
	Version    int    `bun:"version" json:"version"` // 1：V1 入口创建，通知用 MD5；2：V2 入口创建，通知用 RSA
	IsTest     bool   `bun:"is_test" json:"is_test"`
	Channel    string `bun:"channel" json:"channel"` // 渠道实例键 ch<编号>
	PayType    string `bun:"pay_type" json:"pay_type"`
	Name       string `bun:"name" json:"name"`
	Money      string `bun:"money" json:"money"`           // 下单时收到的原始金额字符串，通知原样带回
	Amount     int64  `bun:"amount" json:"amount"`         // 订单金额，分
	PayAmount  int64  `bun:"pay_amount" json:"pay_amount"` // 加了尾数的应付金额，分
	NotifyURL  string `bun:"notify_url" json:"notify_url"`
	ReturnURL  string `bun:"return_url" json:"return_url"`
	Param      string `bun:"param" json:"param"`
	ClientIP   string `bun:"client_ip" json:"client_ip"`
	Device     string `bun:"device" json:"device"`
	Status     string `bun:"status" json:"status"`
	CreatedAt  int64  `bun:"created_at" json:"created_at"`
	ExpiresAt  int64  `bun:"expires_at" json:"expires_at"`
	EndedAt    int64  `bun:"ended_at" json:"ended_at"`       // 支付、关闭或过期的时刻
	ReleasedAt int64  `bun:"released_at" json:"released_at"` // 应付金额可以再分配的时刻
	PaidAt     int64  `bun:"paid_at" json:"paid_at"`
	PaidAmount int64  `bun:"paid_amount" json:"paid_amount"`
	TxnID      int64  `bun:"txn_id" json:"txn_id"`
	APITradeNo string `bun:"api_trade_no" json:"api_trade_no"`
	Buyer      string `bun:"buyer" json:"buyer"`
	UpdatedAt  int64  `bun:"updated_at" json:"updated_at"`
}

// 流水状态
const (
	TxnNew     = "new"     // 刚入库，还没对账
	TxnMatched = "matched" // 已关联订单
	TxnAnomaly = "anomaly" // 有待处理或已处理的异常
	TxnIgnored = "ignored" // 支出等不参与匹配的流水
)

// 收支方向
const (
	DirIn  = "in"
	DirOut = "out"
)

type Transaction struct {
	bun.BaseModel `bun:"table:transactions,alias:t"`

	ID              int64  `bun:"id,pk,autoincrement" json:"id"`
	Channel         string `bun:"channel" json:"channel"`
	ExternalID      string `bun:"external_id" json:"external_id"`
	Direction       string `bun:"direction" json:"direction"`
	Amount          int64  `bun:"amount" json:"amount"` // 分，取绝对值
	OccurredAt      int64  `bun:"occurred_at" json:"occurred_at"`
	AlipayOrderNo   string `bun:"alipay_order_no" json:"alipay_order_no"`
	MerchantOrderNo string `bun:"merchant_order_no" json:"merchant_order_no"`
	OtherAccount    string `bun:"other_account" json:"other_account"`
	Memo            string `bun:"memo" json:"memo"`
	BizDesc         string `bun:"biz_desc" json:"biz_desc"`
	Kind            string `bun:"kind" json:"kind"`
	Raw             string `bun:"raw" json:"raw"`
	Fingerprint     string `bun:"fingerprint" json:"fingerprint"`
	State           string `bun:"state" json:"state"`
	OrderID         int64  `bun:"order_id" json:"order_id"`
	CreatedAt       int64  `bun:"created_at" json:"created_at"`
	UpdatedAt       int64  `bun:"updated_at" json:"updated_at"`
}

// 异常种类
const (
	AnomalyAmbiguous = "ambiguous" // 匹配不唯一
	AnomalyDuplicate = "duplicate" // 重复付款
	AnomalyLate      = "late"      // 订单结束后才付款
	AnomalyUnderpaid = "underpaid" // 少付
	AnomalyOverpaid  = "overpaid"  // 多付
	AnomalyUnmatched = "unmatched" // 无法匹配的收入
	AnomalyConflict  = "conflict"  // 同一流水号内容变了
	AnomalyUnlinked  = "unlinked"  // 人工撤销了关联
)

// 异常状态
const (
	AnomalyOpen     = "open"
	AnomalyResolved = "resolved"
)

type Anomaly struct {
	bun.BaseModel `bun:"table:anomalies,alias:an"`

	ID            int64  `bun:"id,pk,autoincrement" json:"id"`
	Kind          string `bun:"kind" json:"kind"`
	TransactionID int64  `bun:"transaction_id" json:"transaction_id"`
	OrderID       int64  `bun:"order_id" json:"order_id"`
	Detail        string `bun:"detail" json:"detail"`
	Status        string `bun:"status" json:"status"`
	CreatedAt     int64  `bun:"created_at" json:"created_at"`
	ResolvedAt    int64  `bun:"resolved_at" json:"resolved_at"`
	ResolvedBy    string `bun:"resolved_by" json:"resolved_by"`
	Note          string `bun:"note" json:"note"`
}

// 通知状态
const (
	NotifyPending = "pending" // 等待发送或重试
	NotifySuccess = "success" // 已送达
	NotifyDead    = "dead"    // 重试用完，等人工重发
)

type Notification struct {
	bun.BaseModel `bun:"table:notifications,alias:n"`

	OrderID   int64  `bun:"order_id,pk" json:"order_id"`
	Status    string `bun:"status" json:"status"`
	Attempts  int    `bun:"attempts" json:"attempts"`
	FirstAt   int64  `bun:"first_at" json:"first_at"`
	NextAt    int64  `bun:"next_at" json:"next_at"`
	LastAt    int64  `bun:"last_at" json:"last_at"`
	LastError string `bun:"last_error" json:"last_error"`
	CreatedAt int64  `bun:"created_at" json:"created_at"`
	UpdatedAt int64  `bun:"updated_at" json:"updated_at"`
}

type NotifyAttempt struct {
	bun.BaseModel `bun:"table:notify_attempts"`

	ID         int64  `bun:"id,pk,autoincrement" json:"id"`
	OrderID    int64  `bun:"order_id" json:"order_id"`
	Manual     bool   `bun:"manual" json:"manual"`
	URL        string `bun:"url" json:"url"`
	HTTPStatus int    `bun:"http_status" json:"http_status"`
	Response   string `bun:"response" json:"response"`
	Error      string `bun:"error" json:"error"`
	Success    bool   `bun:"success" json:"success"`
	DurationMs int64  `bun:"duration_ms" json:"duration_ms"`
	CreatedAt  int64  `bun:"created_at" json:"created_at"`
}

type AuditLog struct {
	bun.BaseModel `bun:"table:audit_logs"`

	ID        int64  `bun:"id,pk,autoincrement" json:"id"`
	Actor     string `bun:"actor" json:"actor"`
	Action    string `bun:"action" json:"action"`
	Target    string `bun:"target" json:"target"`
	Detail    string `bun:"detail" json:"detail"`
	CreatedAt int64  `bun:"created_at" json:"created_at"`
}
