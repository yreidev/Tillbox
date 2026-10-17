package monitor

import (
	"fmt"
	"html"
	"strings"
	"time"

	"tillbox/internal/epay"
	"tillbox/internal/store"
)

// Telegram 消息的排版：第一行是表情符号加粗体标题，明细放进引用块，订单号用等宽字（手机上点一下就复制），
// 最后一行斜体写站点名和时间；要打开的后台页面做成消息下方的按钮。用 Telegram 的 HTML 格式，
// 动态内容一律转义并截短：解析不了的消息 Telegram 整条拒收。

// Message 是一条 Telegram 消息。Text 是 Telegram 的 HTML 格式；Button 不为空时在消息下方放一个打开链接的按钮。
type Message struct {
	Text   string
	Button *Button
}

type Button struct {
	Text string
	URL  string
}

const (
	// maxField 是标题、明细等单项最多保留的字数。
	maxField = 200
	// maxName 是商户、渠道、商品名最多保留的字数。
	maxName = 40
)

func esc(s string) string { return html.EscapeString(s) }

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func bold(s string) string { return "<b>" + esc(s) + "</b>" }

func code(s string) string { return "<code>" + esc(s) + "</code>" }

// quote 把几行（已经是 HTML）放进一个引用块，空行跳过。
func quote(lines ...string) string {
	var keep []string
	for _, l := range lines {
		if l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) == 0 {
		return ""
	}
	return "<blockquote>" + strings.Join(keep, "\n") + "</blockquote>"
}

func money(cents int64) string { return "¥" + epay.FormatCents(cents) }

// card 是一条消息的内容：标题（纯文本，会加粗）、若干块明细（HTML），以及块太多时最后一行怎么写。
type card struct {
	icon, title string
	blocks      []string
	more        func(n int) string
}

// render 拼成完整的消息。test 为真时标题前标“【测试】”。块数最多 maxListed，总长不超过 maxText，其余只计数。
func render(c card, site string, now time.Time, test bool, link *Button) Message {
	title := c.title
	if test {
		title = "【测试】" + title
	}
	lines := []string{c.icon + " " + bold(title)}
	size := len([]rune(lines[0]))
	for i, b := range c.blocks {
		if b == "" {
			continue
		}
		if c.more != nil && (i == maxListed || size+len([]rune(b)) > maxText) {
			lines = append(lines, esc(c.more(len(c.blocks)-i)))
			break
		}
		lines = append(lines, b)
		size += len([]rune(b)) + 1
	}
	lines = append(lines, "<i>"+esc(site)+" · "+now.In(epay.CST).Format("01-02 15:04")+"</i>")
	m := Message{Text: strings.Join(lines, "\n")}
	// Telegram 可能不接受本机或 http 地址做按钮，那样整条消息都会发不出去；这类地址写进正文，不做按钮。
	if link != nil && link.URL != "" {
		if strings.HasPrefix(link.URL, "https://") {
			m.Button = link
		} else {
			m.Text += "\n" + esc(link.Text+"："+link.URL)
		}
	}
	return m
}

// button 把后台路径拼成完整链接；没有配置对外地址时不放链接。
func (s *Service) button(text, path string) *Button {
	if s.PublicURL == "" {
		return nil
	}
	return &Button{Text: text, URL: s.PublicURL + path}
}

// alertCard 把告警的变化拼成卡片：新的和变多的在前，仍未恢复的其次，恢复的最后。
func alertCard(events []event, now time.Time) card {
	icon := func(a *store.Alert) string {
		if a.Level == store.AlertCritical {
			return "🔴"
		}
		return "🟠"
	}
	since := func(a *store.Alert) string { return duration(now.Sub(store.Time(a.StartedAt))) }
	detail := func(a *store.Alert) string { return esc(clip(a.Detail, maxField)) }
	type item struct {
		icon, title string
		lines       []string
	}
	var items []item
	for _, e := range events {
		switch e.kind {
		case "open":
			items = append(items, item{icon(e.alert), e.alert.Title, []string{detail(e.alert)}})
		case "more":
			items = append(items, item{icon(e.alert), e.alert.Title + "（有新增）", []string{detail(e.alert), "已持续 " + since(e.alert)}})
		}
	}
	for _, e := range events {
		if e.kind == "remind" {
			items = append(items, item{"⏰", e.alert.Title, []string{"已持续 " + since(e.alert) + "，还没恢复", detail(e.alert)}})
		}
	}
	resolved := 0
	for _, e := range events {
		if e.kind == "resolved" {
			resolved++
			items = append(items, item{"🟢", "已恢复：" + e.alert.Title, []string{"持续了 " + since(e.alert)}})
		}
	}
	// 只有一条时，它自己就是标题
	if len(items) == 1 {
		it := items[0]
		return card{icon: it.icon, title: clip(it.title, maxField), blocks: []string{quote(it.lines...)}}
	}
	c := card{icon: "🔔", title: fmt.Sprintf("告警 · %d 条变化", len(items)), more: func(n int) string { return fmt.Sprintf("……另有 %d 条", n) }}
	if resolved == len(items) {
		c.icon, c.title = "🟢", fmt.Sprintf("告警 · %d 条已恢复", len(items))
	}
	for _, it := range items {
		c.blocks = append(c.blocks, it.icon+" "+bold(clip(it.title, maxField))+"\n"+quote(it.lines...))
	}
	return c
}

// alertLink：只有一条还没恢复的告警时直接去处理它，否则打开监控页。
func (s *Service) alertLink(events []event) *Button {
	if len(events) == 1 && events[0].kind != "resolved" && events[0].alert.Link != "" {
		return s.button("去处理", events[0].alert.Link)
	}
	return s.button("打开监控页", "/admin/monitor")
}

// paidItem 是到账消息里的一笔订单。
type paidItem struct {
	Paid, Amount      int64
	Merchant, Channel string
	Name, TradeNo     string
	Test              bool
}

func paidCard(items []paidItem) card {
	if len(items) == 1 {
		o := items[0]
		title := "收款到账 · " + money(o.Paid)
		if o.Test {
			title += " · 测试单"
		}
		lines := []string{}
		if o.Paid != o.Amount {
			lines = append(lines, "订单金额　"+money(o.Amount))
		}
		lines = append(lines,
			"商户　"+esc(clip(o.Merchant, maxName)),
			"渠道　"+esc(clip(o.Channel, maxName)),
			"商品　"+esc(clip(o.Name, maxName)),
			"订单　"+code(o.TradeNo))
		return card{icon: "🟢", title: title, blocks: []string{quote(lines...)}}
	}
	var total int64
	blocks := make([]string, 0, len(items))
	for _, o := range items {
		total += o.Paid
		head := bold(money(o.Paid))
		if o.Paid != o.Amount {
			head += "（订单 " + money(o.Amount) + "）"
		}
		head += " · " + esc(clip(o.Merchant, maxName)) + " · " + esc(clip(o.Channel, maxName))
		if o.Test {
			head += " · 测试单"
		}
		blocks = append(blocks, quote(head, esc(clip(o.Name, maxName))+" · "+code(o.TradeNo)))
	}
	return card{
		icon: "🟢", title: fmt.Sprintf("收款到账 · %d 笔共 %s", len(items), money(total)), blocks: blocks,
		more: func(n int) string { return fmt.Sprintf("……另有 %d 笔", n) },
	}
}

// claimItem 是申诉消息里的一个申诉。TradeNo 为空表示没关联到订单。
type claimItem struct {
	Kind    string
	Amount  int64
	TradeNo string
}

func claimKind(k string) string {
	if s := claimKinds[k]; s != "" {
		return s
	}
	return k
}

func claimOrder(no string) string {
	if no == "" {
		return "没关联到订单"
	}
	return code(no)
}

func claimCard(items []claimItem) card {
	if len(items) == 1 {
		c := items[0]
		return card{icon: "📨", title: "付款申诉 · " + claimKind(c.Kind), blocks: []string{
			quote("申报金额　"+money(c.Amount), "订单　"+claimOrder(c.TradeNo)),
		}}
	}
	blocks := make([]string, 0, len(items))
	for _, c := range items {
		blocks = append(blocks, quote(bold(claimKind(c.Kind))+" · "+money(c.Amount), "订单　"+claimOrder(c.TradeNo)))
	}
	return card{
		icon: "📨", title: fmt.Sprintf("付款申诉 · %d 个", len(items)), blocks: blocks,
		more: func(n int) string { return fmt.Sprintf("……另有 %d 个", n) },
	}
}
