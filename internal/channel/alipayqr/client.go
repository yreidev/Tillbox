// Package alipayqr 是支付宝经营码渠道：收款用经营码（静态码），确认到账靠开放平台 V3 账务明细查询。
//
// V3 的请求签名和响应验签是自己实现的。现成的 Go SDK 在公钥模式下不验响应签名，
// 也拿不到原始响应体，不能用（见 docs/PLAN.md）。
package alipayqr

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/epay"
)

const accountLogPath = "/v3/alipay/data/bill/accountlog/query"

// PageSize 是每页条数（接口允许 1000–2000）。
const PageSize = 2000

// Client 调支付宝开放平台 V3 接口（公钥模式）。
type Client struct {
	Gateway         string
	AppID           string
	PrivateKey      *rsa.PrivateKey
	AlipayPublicKey *rsa.PublicKey
	HTTP            *http.Client
	Now             func() time.Time
}

// AccountLogItem 是一条账务明细。字段都是字符串，原样保留。
type AccountLogItem struct {
	AccountLogID        string `json:"account_log_id"`
	AlipayOrderNo       string `json:"alipay_order_no"`
	Balance             string `json:"balance"`
	BillSource          string `json:"bill_source"`
	BizDesc             string `json:"biz_desc"`
	BizNos              string `json:"biz_nos"`
	BizOrigNo           string `json:"biz_orig_no"`
	ComplementInfo      string `json:"complement_info"`
	Direction           string `json:"direction"`
	MerchantOrderNo     string `json:"merchant_order_no"`
	MerchantOutRefundNo string `json:"merchant_out_refund_no"`
	OtherAccount        string `json:"other_account"`
	StoreName           string `json:"store_name"`
	TransAmount         string `json:"trans_amount"`
	TransDt             string `json:"trans_dt"`
	TransMemo           string `json:"trans_memo"`
	Type                string `json:"type"`
	Raw                 string `json:"-"`
	DecodeError         string `json:"-"` // 这一条的 JSON 读不了时的原因，其他字段只有流水号
}

// AccountLogPage 是一页查询结果。
type AccountLogPage struct {
	Items     []AccountLogItem
	PageNo    int
	PageSize  int
	TotalSize int
}

// APIError 是支付宝返回的错误。
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("支付宝返回错误（HTTP %d）：%s %s", e.Status, e.Code, e.Message)
}

// escape 和官方 SDK 一样：URLEncoder 编码后把 + 换成 %20。
func escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func nonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Sign 生成 Authorization 头。待签名串：认证串\n方法\nURI（含查询串）\n请求体\n。
func (c *Client) Sign(method, uri, body string) (string, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	auth := "app_id=" + c.AppID + ",nonce=" + nonce() + ",timestamp=" + strconv.FormatInt(now().UnixMilli(), 10)
	content := auth + "\n" + method + "\n" + uri + "\n" + body + "\n"
	sig, err := epay.RSASign(content, c.PrivateKey)
	if err != nil {
		return "", err
	}
	return "ALIPAY-SHA256withRSA " + auth + ",sign=" + sig, nil
}

// VerifyResponse 用支付宝公钥验响应：alipay-timestamp\nalipay-nonce\n响应体\n。
func (c *Client) VerifyResponse(h http.Header, body []byte) error {
	sig := h.Get("alipay-signature")
	if sig == "" || sig == "null" {
		return errors.New("支付宝响应没有签名")
	}
	content := h.Get("alipay-timestamp") + "\n" + h.Get("alipay-nonce") + "\n" + string(body) + "\n"
	if !epay.RSAVerify(content, sig, c.AlipayPublicKey) {
		return errors.New("支付宝响应验签失败，请检查支付宝公钥是否填对")
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, query [][2]string) ([]byte, error) {
	var qs strings.Builder
	for i, kv := range query {
		if i > 0 {
			qs.WriteByte('&')
		}
		qs.WriteString(escape(kv[0]) + "=" + escape(kv[1]))
	}
	uri := path
	if qs.Len() > 0 {
		uri += "?" + qs.String()
	}
	authz, err := c.Sign(http.MethodGet, uri, "")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Gateway, "/")+uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authz)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("alipay-request-id", nonce())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求支付宝失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("读取支付宝响应失败：%w", err)
	}
	if resp.StatusCode == http.StatusOK {
		if err := c.VerifyResponse(resp.Header, body); err != nil {
			return nil, err
		}
		return body, nil
	}
	// 错误响应只在带签名时验签（和官方 SDK 一致）；验不过的一律当作不可信。
	if s := resp.Header.Get("alipay-signature"); s != "" && s != "null" {
		if err := c.VerifyResponse(resp.Header, body); err != nil {
			return nil, err
		}
	}
	apiErr := &APIError{Status: resp.StatusCode}
	_ = json.Unmarshal(body, apiErr)
	if apiErr.Code == "" && apiErr.Message == "" {
		apiErr.Message = strings.TrimSpace(string(body[:min(len(body), 200)]))
	}
	return nil, classify(apiErr, resp.Header)
}

// classify 把错误归成凭据/权限错误、限流和其他错误。
// 支付宝 V3 的错误码清单没有完整公开，这里按 HTTP 状态码和错误码里的关键词判断（未确认）。
func classify(e *APIError, h http.Header) error {
	code := strings.ToUpper(e.Code)
	switch {
	case e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden,
		strings.Contains(code, "PERMISSION"), strings.Contains(code, "AUTH"),
		strings.Contains(code, "SIGN"), strings.Contains(code, "APP_ID"), strings.Contains(code, "APPID"):
		return fmt.Errorf("%w：%v", channel.ErrAuth, e) //nolint:errorlint // 只让调用方按凭据错误处理，原始错误只用于展示
	case e.Status == http.StatusTooManyRequests, strings.Contains(code, "LIMIT"), strings.Contains(code, "TOO_MANY"):
		after := time.Minute
		if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s > 0 {
			after = time.Duration(s) * time.Second
		}
		return &channel.RetryAfterError{After: after, Err: e}
	}
	return e
}

// QueryAccountLog 查一页账务明细。时间按北京时间，窗口左闭右开。
func (c *Client) QueryAccountLog(ctx context.Context, start, end time.Time, pageNo int) (*AccountLogPage, error) {
	body, err := c.get(ctx, accountLogPath, [][2]string{
		{"start_time", start.In(epay.CST).Format(time.DateTime)},
		{"end_time", end.In(epay.CST).Format(time.DateTime)},
		{"page_no", strconv.Itoa(pageNo)},
		{"page_size", strconv.Itoa(PageSize)},
	})
	if err != nil {
		return nil, err
	}
	var raw struct {
		DetailList []json.RawMessage `json:"detail_list"`
		PageNo     flexString        `json:"page_no"`
		PageSize   flexString        `json:"page_size"`
		TotalSize  flexString        `json:"total_size"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("支付宝响应格式不对：%w", err)
	}
	page := &AccountLogPage{}
	page.PageNo, _ = strconv.Atoi(string(raw.PageNo))
	page.PageSize, _ = strconv.Atoi(string(raw.PageSize))
	page.TotalSize, _ = strconv.Atoi(string(raw.TotalSize))
	for _, r := range raw.DetailList {
		var it AccountLogItem
		if err := json.Unmarshal(r, &it); err != nil {
			// 一条读不了不影响整页：留下原文和流水号，由渠道记成解析不了的流水。
			it = AccountLogItem{AccountLogID: looseLogID(r), DecodeError: err.Error()}
		}
		it.Raw = string(r)
		page.Items = append(page.Items, it)
	}
	return page, nil
}

// flexString 接受 JSON 字符串、数字或 null。官方 SDK 的模型里这些字段都是字符串，
// 但它用的 Gson 对数字也照收，这里同样放宽，免得实际返回数字时整页解析失败。
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case bytes.Equal(b, []byte("null")):
		*f = ""
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
	case len(b) > 0 && (b[0] == '{' || b[0] == '['):
		// 对象和数组原样保留成 JSON 文本。
		*f = flexString(b)
	default:
		var n json.Number
		if err := json.Unmarshal(b, &n); err != nil {
			return fmt.Errorf("应该是字符串或数字：%s", b)
		}
		*f = flexString(n.String())
	}
	return nil
}

// looseLogID 从读不了的一条明细里尽量取出流水号，取不到时返回空。
func looseLogID(b []byte) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	var id flexString
	if json.Unmarshal(m["account_log_id"], &id) != nil {
		return ""
	}
	return strings.TrimSpace(string(id))
}

// UnmarshalJSON 逐个字段按 flexString 读，未知字段忽略。
func (it *AccountLogItem) UnmarshalJSON(b []byte) error {
	var m map[string]flexString
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k, p := range map[string]*string{
		"account_log_id": &it.AccountLogID, "alipay_order_no": &it.AlipayOrderNo, "balance": &it.Balance,
		"bill_source": &it.BillSource, "biz_desc": &it.BizDesc, "biz_nos": &it.BizNos, "biz_orig_no": &it.BizOrigNo,
		"complement_info": &it.ComplementInfo, "direction": &it.Direction, "merchant_order_no": &it.MerchantOrderNo,
		"merchant_out_refund_no": &it.MerchantOutRefundNo, "other_account": &it.OtherAccount, "store_name": &it.StoreName,
		"trans_amount": &it.TransAmount, "trans_dt": &it.TransDt, "trans_memo": &it.TransMemo, "type": &it.Type,
	} {
		*p = string(m[k])
	}
	return nil
}

// ParseYuan 把元（可带负号、最多两位小数）换算成分。
func ParseYuan(s string) (int64, error) {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" || len(frac) > 2 || strings.Trim(intPart+frac, "0123456789") != "" || len(intPart) > 12 {
		return 0, fmt.Errorf("金额格式不对：%q", s)
	}
	n, _ := strconv.ParseInt(intPart, 10, 64)
	frac += "00"
	f, _ := strconv.ParseInt(frac[:2], 10, 64)
	n = n*100 + f
	if neg {
		n = -n
	}
	return n, nil
}
