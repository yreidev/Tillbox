package epay

import (
	"fmt"
	"strings"
	"time"
)

// MaxAmount 是单笔订单金额上限（分），对应官方 decimal(10,2) 能存的最大值。
const MaxAmount = 99999999_99

// ParseMoney 按官方规则校验金额并换算成分：只能有数字和一个点、大于 0；
// 超过两位小数时四舍五入到分（官方存 decimal(10,2) 时也是这样）。
func ParseMoney(s string) (int64, bool) {
	if s == "" || strings.Trim(s, "0123456789.") != "" || strings.Count(s, ".") > 1 {
		return 0, false
	}
	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return 0, false
	}
	intPart = strings.TrimLeft(intPart, "0")
	if len(intPart) > 8 {
		return 0, false
	}
	var cents int64
	for _, c := range intPart {
		cents = cents*10 + int64(c-'0')
	}
	cents *= 100
	frac += "000"
	cents += int64(frac[0]-'0')*10 + int64(frac[1]-'0')
	if frac[2] >= '5' {
		cents++
	}
	if cents <= 0 || cents > MaxAmount {
		return 0, false
	}
	return cents, true
}

// FormatCents 把分格式化成两位小数的元，例如 1003 → "10.03"。
func FormatCents(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

// CST 是官方服务端用的时区（北京时间）。订单号和对外的时间字段都按它算。
var CST = time.FixedZone("CST", 8*3600)

// FormatTime 格式化成官方的 Y-m-d H:i:s。零值返回空串。
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(CST).Format(time.DateTime)
}
