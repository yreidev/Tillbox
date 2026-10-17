// Package notify 负责支付结果通知（异步通知和跳回 return_url 的参数）。
package notify

import (
	"context"
	"crypto/rsa"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tillbox/internal/epay"
	"tillbox/internal/merchant"
	"tillbox/internal/order"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// Signer 生成通知参数。每次调用都重新签名，V2 订单带上当时的时间戳。
type Signer struct {
	Merchants *merchant.Service
	Settings  *settings.Service
}

// Params 生成某个已支付订单的通知参数（含签名）。
// V1 订单：MD5 签名，不带 timestamp；V2 订单：平台 RSA 签名，带 timestamp。
func (s *Signer) Params(ctx context.Context, o *store.Order, now time.Time) (epay.Params, error) {
	m, err := s.Merchants.Get(ctx, o.MerchantID)
	if err != nil {
		return nil, err
	}
	p := epay.Params{
		"pid":          strconv.FormatInt(o.MerchantID, 10),
		"trade_no":     o.TradeNo,
		"out_trade_no": o.OutTradeNo,
		"type":         o.PayType,
		"name":         o.Name,
		"money":        o.Money,
		"trade_status": "TRADE_SUCCESS",
	}
	// 官方用 empty() 判断这几个可选字段，"0" 也不带。
	if !epay.PHPEmpty(o.Param) {
		p["param"] = o.Param
	}
	if o.Version != order.V2 {
		p["sign"] = epay.MD5Sign(p, m.Key)
		p["sign_type"] = "MD5"
		return p, nil
	}
	if !epay.PHPEmpty(o.APITradeNo) {
		p["api_trade_no"] = o.APITradeNo
	}
	if !epay.PHPEmpty(o.Buyer) {
		p["buyer"] = o.Buyer
	}
	p["timestamp"] = strconv.FormatInt(now.Unix(), 10)
	key, err := s.platformKey(ctx)
	if err != nil {
		return nil, err
	}
	sign, err := epay.RSASign(epay.SignContent(p), key)
	if err != nil {
		return nil, err
	}
	p["sign"] = sign
	p["sign_type"] = "RSA"
	return p, nil
}

func (s *Signer) platformKey(ctx context.Context) (*rsa.PrivateKey, error) {
	_, priv, err := s.Settings.PlatformKey(ctx)
	if err != nil {
		return nil, err
	}
	return epay.ParsePrivateKey(priv)
}

// AppendQuery 把参数拼到地址后面：地址里已有 ? 就用 & 连接，否则用 ?。
func AppendQuery(base string, p epay.Params) string {
	v := url.Values{}
	for k, val := range p {
		v.Set(k, val)
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + v.Encode()
}
