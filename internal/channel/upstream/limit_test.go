package upstream

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/store"
)

// 上游拒单原话里的限额：认得出的记下订单超出的那一边，金额其实没超出的不算。
func TestAmountLimit(t *testing.T) {
	for _, c := range []struct {
		msg      string
		amount   int64
		min, max int64
	}{
		{"最小支付金额是0.3元", 1, 30, 0},
		{"<center>当前支付方式单笔最小限额为1元，请选择其他支付方式！</center>", 50, 100, 0},
		{"最低支付金额为 0.50 元", 10, 50, 0},
		{"金额不能小于2元", 100, 200, 0},
		{"最大支付金额是5000元", 600000, 0, 500000},
		{"当前支付方式单笔最大限额为200元，请选择其他支付方式！", 30000, 0, 20000},
		{"单笔金额不能超过1000元", 200000, 0, 100000},
		{"最小支付金额是0.3元", 30, 0, 0},
		{"最大支付金额是5000元", 500000, 0, 0},
		{"商户未开通该支付方式", 1, 0, 0},
		{"支付方式维护中", 1, 0, 0},
	} {
		var lo, hi int64
		if ae := amountLimit("alipay", c.amount, &Error{Code: "-2", Msg: c.msg}); ae != nil {
			lo, hi = ae.Min, ae.Max
		}
		if lo != c.min || hi != c.max {
			t.Errorf("%q（%d 分）：最低 %d、最高 %d", c.msg, c.amount, lo, hi)
		}
	}
}

// 预下单被上游以金额为由拒绝：返回带上游原话的 *channel.AmountError。
func TestPrepayAmountRejected(t *testing.T) {
	s := newStub(t)
	c := newChannel(t, s, 1)
	s.reply = func(string, epay.Params) map[string]any {
		return map[string]any{"code": -2, "msg": "最小支付金额是20元"}
	}
	_, err := c.Prepay(context.Background(), testOrder, "https://tillbox.example.com")
	var ae *channel.AmountError
	if !errors.As(err, &ae) || ae.Min != 2000 || ae.Type != "wxpay" || ae.Message() != "微信支付单笔最低 20.00 元" || !strings.Contains(err.Error(), "最小支付金额是20元") {
		t.Fatalf("预下单：%v", err)
	}
	s.reply = func(string, epay.Params) map[string]any {
		return map[string]any{"code": -1, "msg": "最小支付金额是5元"}
	}
	if _, err := c.Prepay(context.Background(), testOrder, "https://tillbox.example.com"); errors.As(err, &ae) {
		t.Errorf("金额没低于限额时不算限额错误：%v", err)
	}
}

func rejected(typ string, amount int64, msg string) error {
	if ae := amountLimit(typ, amount, &Error{Code: "-2", Msg: msg}); ae != nil {
		return ae
	}
	return &Error{Code: "-2", Msg: msg}
}

// 自动识别的限额：被拒时记下，24 小时内提前拦下，之后放行，下单成功就放宽；手动设置的不过期。
func TestLearnLimits(t *testing.T) {
	b := box(t)
	raw, err := merge(t, b, "", Input{APIURL: "https://pay.example.com", Version: 1, PID: "1001", Key: "k123", Types: []string{"alipay", "wxpay"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	c := New(b)
	apply := func(cfg string) {
		t.Helper()
		if cfg == "" {
			t.Fatal("应该改配置")
		}
		raw = cfg
		c.Configure(store.Channel{ID: 7, Type: store.ChannelEpay, Enabled: true, Config: raw})
	}
	apply(raw)
	t0 := time.Date(2026, 10, 17, 16, 15, 43, 0, time.UTC)
	if err := c.CheckAmount("alipay", 1, t0); err != nil {
		t.Fatalf("没有限额时不拦：%v", err)
	}

	apply(Learn(raw, "alipay", 1, rejected("alipay", 1, "最小支付金额是0.3元"), t0))
	var ae *channel.AmountError
	if err := c.CheckAmount("alipay", 29, t0.Add(time.Hour)); !errors.As(err, &ae) || ae.Min != 30 || ae.Err != nil || ae.Message() != "支付宝单笔最低 0.30 元" {
		t.Fatalf("记下限额后：%v", err)
	}
	if err := c.CheckAmount("alipay", 30, t0); err != nil {
		t.Errorf("正好最低金额：%v", err)
	}
	if err := c.CheckAmount("wxpay", 1, t0); err != nil {
		t.Errorf("别的支付方式不受影响：%v", err)
	}
	if v := ViewOf(raw).Limits["alipay"]; v.Min != "0.30" || v.Max != "" || v.MinAt != store.Ms(t0) {
		t.Errorf("后台看到的限额：%+v", v)
	}
	for name, cfg := range map[string]string{
		"成功且没超出":   Learn(raw, "alipay", 50, nil, t0),
		"别的错误":     Learn(raw, "alipay", 1, errors.New("连不上上游"), t0),
		"认不出的拒绝":   Learn(raw, "alipay", 1, rejected("alipay", 1, "支付方式维护中"), t0),
		"提前拦下的":    Learn(raw, "alipay", 1, &channel.AmountError{Type: "alipay", Min: 30}, t0),
		"没开放的方式":   Learn(raw, "qqpay", 1, rejected("qqpay", 1, "最小支付金额是0.3元"), t0),
		"同一时刻再记一次": Learn(raw, "alipay", 1, rejected("alipay", 1, "最小支付金额是0.3元"), t0),
	} {
		if cfg != "" {
			t.Errorf("%s不用改配置", name)
		}
	}

	// 过了 LimitTrust 不再拦，交给上游；上游已经放宽、下单成功，就清掉。
	t1 := t0.Add(LimitTrust)
	if err := c.CheckAmount("alipay", 1, t1); err != nil {
		t.Errorf("过期的自动限额不拦：%v", err)
	}
	apply(Learn(raw, "alipay", 1, nil, t1))
	if _, ok := ViewOf(raw).Limits["alipay"]; ok {
		t.Errorf("下单成功后应放宽：%+v", ViewOf(raw).Limits)
	}

	// 手动设置的不过期；上游拒绝时换成上游给的值，最高金额低于新的最低金额时去掉。
	cfg, err := merge(t, b, raw, Input{APIURL: "https://pay.example.com", Version: 1, PID: "1001", Types: []string{"alipay", "wxpay"},
		Limits: map[string]LimitInput{"alipay": {Min: "1", Max: "1.5"}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	apply(cfg)
	if err := c.CheckAmount("alipay", 99, t1.Add(1000*time.Hour)); !errors.As(err, &ae) || ae.Min != 100 {
		t.Errorf("手动设置的最低金额：%v", err)
	}
	if err := c.CheckAmount("alipay", 151, t1); !errors.As(err, &ae) || ae.Max != 150 || ae.Message() != "支付宝单笔最高 1.50 元" {
		t.Errorf("手动设置的最高金额：%v", err)
	}
	apply(Learn(raw, "alipay", 150, rejected("alipay", 150, "最小支付金额是2元"), t1))
	if v := ViewOf(raw).Limits["alipay"]; v.Min != "2.00" || v.MinAt != store.Ms(t1) || v.Max != "" {
		t.Errorf("上游拒绝后：%+v", v)
	}
	apply(Learn(raw, "wxpay", 600000, rejected("wxpay", 600000, "当前支付方式单笔最大限额为5000元，请选择其他支付方式！"), t1))
	if err := c.CheckAmount("wxpay", 500001, t1); !errors.As(err, &ae) || ae.Max != 500000 {
		t.Errorf("最高金额：%v", err)
	}
	apply(Learn(raw, "wxpay", 800000, nil, t1.Add(LimitTrust)))
	if _, ok := ViewOf(raw).Limits["wxpay"]; ok {
		t.Errorf("超过最高金额的下单成功后应放宽：%+v", ViewOf(raw).Limits)
	}
}

// 后台改限额：没改的那一边保留自动识别的时刻；不传限额时原样保留；没开放的方式去掉。
func TestMergeLimits(t *testing.T) {
	b := box(t)
	base := Input{APIURL: "https://pay.example.com", Version: 1, PID: "1001", Key: "k123", Types: []string{"alipay", "wxpay"}}
	raw, err := merge(t, b, "", base, true)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 17, 16, 15, 43, 0, time.UTC)
	raw = Learn(raw, "alipay", 1, rejected("alipay", 1, "最小支付金额是0.3元"), at)
	in := base
	in.Key = ""

	in.Limits = map[string]LimitInput{"alipay": {Min: "0.30", Max: "500"}, "wxpay": {Min: " ", Max: ""}}
	cfg, err := merge(t, b, raw, in, true)
	if v := ViewOf(cfg).Limits; err != nil || len(v) != 1 || v["alipay"] != (LimitView{Min: "0.30", Max: "500.00", MinAt: store.Ms(at)}) {
		t.Fatalf("加最高金额：%v %+v", err, v)
	}
	in.Limits = map[string]LimitInput{"alipay": {Min: "0.5", Max: "500"}}
	cfg, err = merge(t, b, cfg, in, true)
	if v := ViewOf(cfg).Limits["alipay"]; err != nil || v != (LimitView{Min: "0.50", Max: "500.00"}) {
		t.Fatalf("改最低金额算手动设置：%v %+v", err, v)
	}
	in.Limits = nil
	cfg, err = merge(t, b, cfg, in, true)
	if v := ViewOf(cfg).Limits["alipay"]; err != nil || v.Min != "0.50" || v.Max != "500.00" {
		t.Fatalf("不传限额时保留：%v %+v", err, v)
	}
	in.Types = []string{"wxpay"}
	cfg, err = merge(t, b, cfg, in, true)
	if c, _ := ParseConfig(cfg); err != nil || c.Limits != nil {
		t.Fatalf("没开放的方式去掉限额：%v %+v", err, c.Limits)
	}
	for _, x := range []struct {
		lim  LimitInput
		want string
	}{
		{LimitInput{Min: "abc"}, "支付宝的单笔最低金额格式不对"},
		{LimitInput{Max: "-1"}, "支付宝的单笔最高金额格式不对"},
		{LimitInput{Min: "5", Max: "1"}, "不能高于"},
	} {
		in := base
		in.Limits = map[string]LimitInput{"alipay": x.lim}
		if _, err := merge(t, b, raw, in, true); err == nil || !strings.Contains(err.Error(), x.want) {
			t.Errorf("%+v：%v", x.lim, err)
		}
	}
}
