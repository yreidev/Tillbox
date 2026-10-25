package monitor

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"tillbox/internal/store"
)

// balanced 检查 Telegram HTML 里用到的标签成对出现，否则 Telegram 整条拒收。
func balanced(t *testing.T, text string) {
	t.Helper()
	for _, tag := range []string{"b", "i", "code", "blockquote"} {
		if o, c := strings.Count(text, "<"+tag+">"), strings.Count(text, "</"+tag+">"); o != c {
			t.Fatalf("<%s> 不成对（%d/%d）：\n%s", tag, o, c, text)
		}
	}
}

// 第三方平台错误原话、商品名等动态内容可能带 < > &，要转义；太长的截短。
func TestRenderEscapes(t *testing.T) {
	now := time.Date(2026, 10, 17, 13, 15, 0, 0, time.UTC)
	a := &store.Alert{Level: store.AlertCritical, Title: "渠道<1>&查账失败", Detail: "<html>" + strings.Repeat("长", 500), StartedAt: store.Ms(now)}
	m := render(alertCard([]event{{a, "open"}}, now), "站<点>", now, false, nil)
	if strings.Contains(m.Text, "<html>") || strings.Contains(m.Text, "<1>") || !strings.Contains(m.Text, "渠道&lt;1&gt;&amp;查账失败") {
		t.Fatalf("没转义：\n%s", m.Text)
	}
	if !strings.Contains(m.Text, "<i>站&lt;点&gt; · 10-17 21:15</i>") {
		t.Fatalf("最后一行是站点名和北京时间：\n%s", m.Text)
	}
	if strings.Count(m.Text, "长") != maxField-len([]rune("<html>")) {
		t.Fatalf("明细应截短到 %d 字：\n%s", maxField, m.Text)
	}
	balanced(t, m.Text)

	items := []paidItem{{Paid: 100, Amount: 100, Merchant: "A&B", Channel: "<渠道>", Name: "x<y", TradeNo: "1"}}
	m = render(paidCard(items), "Tillbox", now, false, &Button{Text: "查看订单", URL: "https://pay.example.com/admin/orders?q=1"})
	for _, want := range []string{"A&amp;B", "&lt;渠道&gt;", "x&lt;y"} {
		if !strings.Contains(m.Text, want) {
			t.Fatalf("应有 %q：\n%s", want, m.Text)
		}
	}
	if m.Button == nil || m.Button.URL != "https://pay.example.com/admin/orders?q=1" {
		t.Fatalf("https 地址做成按钮：%+v", m.Button)
	}
	balanced(t, m.Text)
}

// 一次到账很多笔时最多列 maxListed 笔，其余计数；总长留在上限以内。
func TestRenderFits(t *testing.T) {
	now := time.Now()
	var items []paidItem
	for i := range 15 {
		items = append(items, paidItem{Paid: 100, Amount: 100, Merchant: strings.Repeat("商", 100), Channel: "渠道", Name: strings.Repeat("品", 100), TradeNo: fmt.Sprint(i)})
	}
	m := render(paidCard(items), "Tillbox", now, false, nil)
	if !strings.Contains(m.Text, "15 笔共 ¥15.00") || !strings.Contains(m.Text, "……另有 5 笔") || len([]rune(m.Text)) > maxText+100 {
		t.Fatalf("应列 10 笔、其余计数（%d 字）：\n%s", len([]rune(m.Text)), m.Text)
	}
	balanced(t, m.Text)

	var events []event
	for i := range 3 {
		events = append(events, event{&store.Alert{Level: store.AlertWarning, Title: fmt.Sprintf("问题 %d", i), StartedAt: store.Ms(now)}, "resolved"})
	}
	m = render(alertCard(events, now), "Tillbox", now, false, nil)
	if !strings.HasPrefix(m.Text, "🟢 <b>告警 · 3 条已恢复</b>") {
		t.Fatalf("全是恢复时标题写已恢复：\n%s", m.Text)
	}
	balanced(t, m.Text)
}
