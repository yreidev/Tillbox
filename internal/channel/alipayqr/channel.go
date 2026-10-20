package alipayqr

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/secret"
	"tillbox/internal/store"
)

// 支付宝网关
const (
	GatewayProd    = "https://openapi.alipay.com"
	GatewaySandbox = "https://openapi-sandbox.dl.alipaydev.com"
)

// Config 是经营码渠道的配置，存在渠道表的 config 列。
type Config struct {
	AppID           string `json:"app_id"`
	AppPrivateKey   string `json:"app_private_key"` // 主密钥加密
	AppPublicKey    string `json:"app_public_key"`  // 交给用户上传到支付宝
	AlipayPublicKey string `json:"alipay_public_key"`
	Gateway         string `json:"gateway"`
	QRCodeURL       string `json:"qrcode_url"` // 经营码内容，https://qr.alipay.com/...
	PayeeName       string `json:"payee_name"` // 收银台显示的收款方名称，可空
}

// ParseConfig 解析渠道表里的配置，没填网关时用正式环境。
func ParseConfig(raw string) (Config, error) {
	c := Config{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return c, fmt.Errorf("经营码渠道配置格式不对：%w", err)
		}
	}
	if c.Gateway == "" {
		c.Gateway = GatewayProd
	}
	return c, nil
}

// Channel 实现 channel.LedgerSource。后台改了配置后由渠道注册表原地更新。
type Channel struct {
	channel.Base
	box  *secret.Box
	http *http.Client

	mu     sync.RWMutex
	cfg    Config
	cfgErr error
}

func New(box *secret.Box) *Channel {
	return &Channel{
		box: box,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy:           http.ProxyFromEnvironment,
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			},
		},
	}
}

// SetHTTPClient 替换 HTTP 客户端（测试用）。
func (c *Channel) SetHTTPClient(h *http.Client) { c.http = h }

func (c *Channel) Configure(row store.Channel) {
	cfg, err := ParseConfig(row.Config)
	c.mu.Lock()
	c.cfg, c.cfgErr = cfg, err
	c.mu.Unlock()
	c.SetRow(row)
}

// Config 返回当前配置。
func (c *Channel) Config() (Config, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg, c.cfgErr
}

func (c *Channel) PayTypes() []string     { return []string{"alipay"} }
func (c *Channel) MaxSpan() time.Duration { return 24 * time.Hour }

func (c *Channel) Ready(context.Context) error {
	a, err := c.Config()
	if err != nil {
		return err
	}
	if err := c.CheckEnabled(); err != nil {
		return err
	}
	switch {
	case a.QRCodeURL == "":
		return errors.New("还没有设置支付宝经营码")
	case a.AppID == "" || a.AppPrivateKey == "" || a.AlipayPublicKey == "":
		return errors.New("支付宝查账参数没有填完整")
	}
	return nil
}

func (c *Channel) Guide(context.Context, *store.Order) (channel.Guide, error) {
	a, err := c.Config()
	if err != nil {
		return channel.Guide{}, err
	}
	return channel.Guide{QRCode: a.QRCodeURL, PayeeName: a.PayeeName, App: "支付宝", ManualAmount: true}, nil
}

// Client 按当前配置建一个接口客户端。
func (c *Channel) Client(context.Context) (*Client, error) {
	a, err := c.Config()
	if err != nil {
		return nil, err
	}
	if a.AppID == "" || a.AppPrivateKey == "" || a.AlipayPublicKey == "" {
		return nil, channel.ErrNotConfigured
	}
	privText, err := c.box.Open(a.AppPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("应用私钥解密失败：%w", err)
	}
	priv, err := epay.ParsePrivateKey(privText)
	if err != nil {
		return nil, fmt.Errorf("应用私钥无效：%w", err)
	}
	pub, err := epay.ParsePublicKey(a.AlipayPublicKey)
	if err != nil {
		return nil, fmt.Errorf("支付宝公钥无效：%w", err)
	}
	return &Client{Gateway: a.Gateway, AppID: a.AppID, PrivateKey: priv, AlipayPublicKey: pub, HTTP: c.http}, nil
}

// Fetch 拉 [start, end) 的全部流水。一页装不下时把窗口对半切开再查；切到 1 秒还装不下才翻页。
// 解析不了的单条流水带着原因返回（Entry.Invalid），不让整段查账失败。
func (c *Channel) Fetch(ctx context.Context, start, end time.Time) ([]channel.Entry, error) {
	cl, err := c.Client(ctx)
	if err != nil {
		return nil, err
	}
	items, err := fetchWindow(ctx, cl, start.Truncate(time.Second), end.Truncate(time.Second))
	if err != nil {
		return nil, err
	}
	out := make([]channel.Entry, 0, len(items))
	for _, it := range items {
		e, err := toEntry(it)
		if err != nil {
			e = invalidEntry(it, err)
		}
		out = append(out, e)
	}
	return out, nil
}

func fetchWindow(ctx context.Context, cl *Client, start, end time.Time) ([]AccountLogItem, error) {
	if !end.After(start) {
		return nil, nil
	}
	first, err := cl.QueryAccountLog(ctx, start, end, 1)
	if err != nil {
		return nil, err
	}
	if first.TotalSize <= len(first.Items) {
		return first.Items, nil
	}
	if span := end.Sub(start); span > time.Second {
		mid := start.Add(span / 2).Truncate(time.Second)
		if !mid.After(start) {
			mid = start.Add(time.Second)
		}
		left, err := fetchWindow(ctx, cl, start, mid)
		if err != nil {
			return nil, err
		}
		right, err := fetchWindow(ctx, cl, mid, end)
		if err != nil {
			return nil, err
		}
		return append(left, right...), nil
	}
	items := first.Items
	for page := 2; len(items) < first.TotalSize; page++ {
		p, err := cl.QueryAccountLog(ctx, start, end, page)
		if err != nil {
			return nil, err
		}
		if len(p.Items) == 0 {
			break
		}
		items = append(items, p.Items...)
	}
	return items, nil
}

func toEntry(it AccountLogItem) (channel.Entry, error) {
	if it.DecodeError != "" {
		return channel.Entry{}, fmt.Errorf("流水 %s 的格式不对：%s", it.AccountLogID, it.DecodeError)
	}
	amount, err := ParseYuan(it.TransAmount)
	if err != nil {
		return channel.Entry{}, fmt.Errorf("流水 %s：%w", it.AccountLogID, err)
	}
	at, err := time.ParseInLocation(time.DateTime, strings.TrimSpace(it.TransDt), epay.CST)
	if err != nil {
		return channel.Entry{}, fmt.Errorf("流水 %s 的时间格式不对：%q", it.AccountLogID, it.TransDt)
	}
	if it.AccountLogID == "" {
		return channel.Entry{}, errors.New("流水缺少 account_log_id")
	}
	dir := store.DirIn
	switch {
	case it.Direction == "支出":
		dir = store.DirOut
	case it.Direction == "收入":
	case amount < 0:
		dir = store.DirOut
	}
	if amount < 0 {
		amount = -amount
	}
	return channel.Entry{
		ExternalID: it.AccountLogID, Direction: dir, Amount: amount, OccurredAt: at,
		AlipayOrderNo: it.AlipayOrderNo, MerchantOrderNo: it.MerchantOrderNo,
		OtherAccount: it.OtherAccount, Memo: it.TransMemo, BizDesc: it.BizDesc, Kind: it.Type, Raw: it.Raw,
	}, nil
}

// invalidEntry 是解析不了的一条流水：原文和读得出的字段照存，金额和时间读不出来时留空。
// 没有流水号时用原文的摘要代替，同一条再出现时不会重复入库。
func invalidEntry(it AccountLogItem, cause error) channel.Entry {
	id := it.AccountLogID
	if id == "" {
		sum := sha256.Sum256([]byte(it.Raw))
		id = "invalid:" + hex.EncodeToString(sum[:16])
	}
	e := channel.Entry{
		ExternalID: id, Direction: store.DirIn,
		AlipayOrderNo: it.AlipayOrderNo, MerchantOrderNo: it.MerchantOrderNo,
		OtherAccount: it.OtherAccount, Memo: it.TransMemo, BizDesc: it.BizDesc, Kind: it.Type, Raw: it.Raw,
		Invalid: cause.Error(),
	}
	if it.Direction == "支出" {
		e.Direction = store.DirOut
	}
	if amount, err := ParseYuan(it.TransAmount); err == nil {
		e.Amount = max(amount, -amount)
	}
	if at, err := time.ParseInLocation(time.DateTime, strings.TrimSpace(it.TransDt), epay.CST); err == nil {
		e.OccurredAt = at
	}
	return e
}

// Test 用当前配置查最近一分钟的流水，检查凭据和权限。
func (c *Channel) Test(ctx context.Context) error {
	cl, err := c.Client(ctx)
	if err != nil {
		return err
	}
	end := time.Now().Truncate(time.Second)
	_, err = cl.QueryAccountLog(ctx, end.Add(-time.Minute), end, 1)
	return err
}
