package epay

import (
	"testing"
	"time"
)

func TestParseMoney(t *testing.T) {
	ok := map[string]int64{
		"10":           1000,
		"10.00":        1000,
		"10.0":         1000,
		"10.5":         1050,
		"0.01":         1,
		".5":           50,
		"5.":           500,
		"007.10":       710,
		"1.005":        101, // 第三位小数四舍五入
		"1.004":        100,
		"0.009":        1,
		"99999999.99":  MaxAmount,
		"99999999.994": MaxAmount,
	}
	for s, want := range ok {
		got, valid := ParseMoney(s)
		if !valid || got != want {
			t.Errorf("ParseMoney(%q) = %d, %v；应为 %d", s, got, valid, want)
		}
	}
	bad := []string{"", ".", "0", "0.00", "0.004", "-1", "+1", "1e3", "1,000", " 1", "1 ", "1.2.3", "abc", "１", "100000000", "99999999.995", "0x10"}
	for _, s := range bad {
		if c, valid := ParseMoney(s); valid {
			t.Errorf("ParseMoney(%q) 不该通过，得到 %d", s, c)
		}
	}
}

func TestFormatCents(t *testing.T) {
	for c, want := range map[int64]string{0: "0.00", 1: "0.01", 10: "0.10", 1003: "10.03", MaxAmount: "99999999.99", -5: "-0.05"} {
		if got := FormatCents(c); got != want {
			t.Errorf("FormatCents(%d) = %s，应为 %s", c, got, want)
		}
	}
}

func TestFormatTime(t *testing.T) {
	if FormatTime(time.Time{}) != "" {
		t.Error("零值应为空串")
	}
	at := time.Date(2026, 10, 14, 16, 30, 5, 0, time.UTC)
	if got := FormatTime(at); got != "2026-10-15 00:30:05" {
		t.Errorf("应按北京时间格式化，得到 %s", got)
	}
}
