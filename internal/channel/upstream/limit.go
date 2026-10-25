package upstream

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/epay"
	"tillbox/internal/store"
)

// 易支付的商户信息接口不返回限额，第三方平台的单笔限额只能从它拒单的原话里认出来。

// LimitTrust 是自动识别的限额拦多久。过了这段时间不再提前拦下，交给第三方平台判断：再被拒就重新识别，
// 下单成功就放宽。第三方平台调低了限额时，不会一直挡住其实能付的金额。
const LimitTrust = 24 * time.Hour

// Limit 是一种支付方式的单笔金额限制（分），0 表示不限。
// MinAt、MaxAt 是从第三方平台拒单原话里自动识别出来的时刻（毫秒），0 表示后台手动设置。
type Limit struct {
	Min   int64 `json:"min,omitempty"`
	Max   int64 `json:"max,omitempty"`
	MinAt int64 `json:"min_at,omitempty"`
	MaxAt int64 `json:"max_at,omitempty"`
}

// LimitInput 是后台填的限额（元），留空表示不限。
type LimitInput struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

// LimitView 是给后台看的限额（元），不限时为空。
type LimitView struct {
	Min   string `json:"min"`
	Max   string `json:"max"`
	MinAt int64  `json:"min_at"`
	MaxAt int64  `json:"max_at"`
}

// 第三方平台以金额为由拒单的原话，例如彩虹易支付的“最小支付金额是0.3元”“当前支付方式单笔最小限额为0.3元”。
var (
	minRe = regexp.MustCompile(`(?:(?:最小|最低)(?:支付|付款|交易|订单)?(?:金额|限额)(?:是|为|：|:)?|(?:不能|不得|不可)(?:小于|低于))\s*([0-9]+(?:\.[0-9]+)?)\s*元`)
	maxRe = regexp.MustCompile(`(?:(?:最大|最高)(?:支付|付款|交易|订单)?(?:金额|限额)(?:是|为|：|:)?|(?:不能|不得|不可)(?:大于|高于|超过))\s*([0-9]+(?:\.[0-9]+)?)\s*元`)
)

// amountLimit 从第三方平台的拒单原话里认出订单金额超出的那一边限额，认不出或金额其实没超出时返回 nil。
func amountLimit(typ string, amount int64, err *Error) *channel.AmountError {
	if m := minRe.FindStringSubmatch(err.Msg); m != nil {
		if v, ok := epay.ParseMoney(m[1]); ok && amount < v {
			return &channel.AmountError{Type: typ, Min: v, Err: err}
		}
	}
	if m := maxRe.FindStringSubmatch(err.Msg); m != nil {
		if v, ok := epay.ParseMoney(m[1]); ok && amount > v {
			return &channel.AmountError{Type: typ, Max: v, Err: err}
		}
	}
	return nil
}

// limit 返回支付方式现在生效的限额：自动识别超过 LimitTrust 的那一边不算。
func (c Config) limit(typ string, now time.Time) (lo, hi int64) {
	l := c.Limits[typ]
	fresh := func(at int64) bool { return at == 0 || now.Sub(store.Time(at)) < LimitTrust }
	if fresh(l.MinAt) {
		lo = l.Min
	}
	if fresh(l.MaxAt) {
		hi = l.Max
	}
	return lo, hi
}

// CheckAmount 按记下的限额检查金额，实现 channel.Limiter。
func (c *Channel) CheckAmount(typ string, amount int64, now time.Time) error {
	cfg, _ := c.Config()
	lo, hi := cfg.limit(typ, now)
	switch {
	case lo > 0 && amount < lo:
		return &channel.AmountError{Type: typ, Min: lo}
	case hi > 0 && amount > hi:
		return &channel.AmountError{Type: typ, Max: hi}
	}
	return nil
}

// Learn 按一次预下单的结果更新支付方式的限额，返回新的配置原文，不用改时返回空串。
// 第三方平台以金额为由拒单时记下第三方平台给的限额，手动设置的值也覆盖；下单成功而金额超出了自动识别的限额，
// 说明第三方平台已经放宽，清掉那一边。
func Learn(raw, typ string, amount int64, err error, now time.Time) string {
	c, perr := ParseConfig(raw)
	if perr != nil || !slices.Contains(c.Types, typ) {
		return ""
	}
	old := c.Limits[typ]
	l := old
	var ae *channel.AmountError
	switch {
	case err == nil:
		if l.MinAt != 0 && amount < l.Min {
			l.Min, l.MinAt = 0, 0
		}
		if l.MaxAt != 0 && amount > l.Max {
			l.Max, l.MaxAt = 0, 0
		}
	case errors.As(err, &ae) && ae.Err != nil:
		at := store.Ms(now)
		if ae.Min > 0 {
			l.Min, l.MinAt = ae.Min, at
			if l.Max > 0 && l.Max < l.Min {
				l.Max, l.MaxAt = 0, 0
			}
		}
		if ae.Max > 0 {
			l.Max, l.MaxAt = ae.Max, at
			if l.Min > l.Max {
				l.Min, l.MinAt = 0, 0
			}
		}
	}
	if l == old {
		return ""
	}
	if c.Limits == nil {
		c.Limits = map[string]Limit{}
	}
	if l == (Limit{}) {
		delete(c.Limits, typ)
	} else {
		c.Limits[typ] = l
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// mergeLimits 合并后台填的限额，只留开放的支付方式。in 为空（例如卡片上直接启用、停用）时保留原值；
// 和原值一样的那一边保留自动识别的时刻，改过的算手动设置。
func mergeLimits(cur map[string]Limit, in map[string]LimitInput, types []string) (map[string]Limit, error) {
	out := map[string]Limit{}
	for _, t := range types {
		old := cur[t]
		l := old
		if in != nil {
			x := in[t]
			lo, err := limitAmount(t, "最低", x.Min)
			if err != nil {
				return nil, err
			}
			hi, err := limitAmount(t, "最高", x.Max)
			if err != nil {
				return nil, err
			}
			if lo > 0 && hi > 0 && lo > hi {
				return nil, channel.Invalid("%s的单笔最低金额不能高于最高金额", channel.TypeLabel(t))
			}
			l = Limit{Min: lo, Max: hi}
			if lo == old.Min {
				l.MinAt = old.MinAt
			}
			if hi == old.Max {
				l.MaxAt = old.MaxAt
			}
		}
		if l != (Limit{}) {
			out[t] = l
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func limitAmount(typ, side, s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	v, ok := epay.ParseMoney(s)
	if !ok {
		return 0, channel.Invalid("%s的单笔%s金额格式不对：填多少元，不限就留空", channel.TypeLabel(typ), side)
	}
	return v, nil
}

func limitViews(c Config) map[string]LimitView {
	out := map[string]LimitView{}
	yuan := func(v int64) string {
		if v == 0 {
			return ""
		}
		return epay.FormatCents(v)
	}
	for _, t := range c.Types {
		if l, ok := c.Limits[t]; ok {
			out[t] = LimitView{Min: yuan(l.Min), Max: yuan(l.Max), MinAt: l.MinAt, MaxAt: l.MaxAt}
		}
	}
	return out
}
