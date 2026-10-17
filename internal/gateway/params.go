// Package gateway 实现易支付 V1（MD5）和 V2（RSA）的对外接口。
// 行为按官方服务端对齐，规格见 docs/research/epay-protocol.md，有意不同的地方见 epay-compat.md。
package gateway

import (
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"tillbox/internal/epay"
)

// maxFormBytes 是表单正文的上限。
const maxFormBytes = 1 << 20

// flatten 把 url.Values 转成参数表：同名参数取最后一个（和 PHP 一样），带 [ 的参数在 PHP 里是数组，不参与签名，直接丢掉。
func flatten(v url.Values) epay.Params {
	p := epay.Params{}
	for k, vs := range v {
		if len(vs) == 0 || strings.Contains(k, "[") {
			continue
		}
		p[k] = vs[len(vs)-1]
	}
	return p
}

// postForm 读 POST 表单（urlencoded 或 multipart），不含查询串。
func postForm(r *http.Request) url.Values {
	if r.Method != http.MethodPost {
		return url.Values{}
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	r.Body = http.MaxBytesReader(nil, r.Body, maxFormBytes)
	if ct == "multipart/form-data" {
		if err := r.ParseMultipartForm(maxFormBytes); err != nil || r.MultipartForm == nil {
			return url.Values{}
		}
		return url.Values(r.MultipartForm.Value)
	}
	if err := r.ParseForm(); err != nil {
		return url.Values{}
	}
	return r.PostForm
}

// merged 读查询串和表单，同名时以表单为准。
func merged(r *http.Request) epay.Params {
	p := flatten(r.URL.Query())
	for k, v := range flatten(postForm(r)) {
		p[k] = v
	}
	return p
}

// phpEmpty 对应 PHP 的 empty() 用在字符串上："" 和 "0" 都算空。
func phpEmpty(s string) bool { return epay.PHPEmpty(s) }

// intval 对应 PHP 的 intval()：取开头的整数部分，取不到为 0。
func intval(s string) int64 {
	s = strings.TrimLeft(s, " \t\n\r\v\f")
	end := 0
	if end < len(s) && (s[end] == '+' || s[end] == '-') {
		end++
	}
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, _ := strconv.ParseInt(s[:end], 10, 64)
	return n
}

var outTradeNoRe = regexp.MustCompile(`^[a-zA-Z0-9._\-|]+$`)
var urlRe = regexp.MustCompile(`^(http|https)://[^\s]+`)

// isURL 对应官方的 is_url：http(s):// 开头，后面紧跟非空白字符。
func isURL(s string) bool { return urlRe.MatchString(s) }

// cutUTF8 按 UTF-8 字符边界截到不超过 n 字节（对应 mb_strcut）。
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// phpFloat 把分转成 PHP 输出浮点数的样子：10.50 → "10.5"，10.00 → "10"。
func phpFloat(cents int64) string {
	s := epay.FormatCents(cents)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
