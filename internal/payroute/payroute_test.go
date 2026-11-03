package payroute

import (
	"errors"
	"testing"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/store"
)

// limited 是有单笔上限的渠道（只实现 Pick 用到的 Limiter）。
type limited struct {
	channel.Instance
	max int64
}

func (l limited) CheckAmount(typ string, amount int64, _ time.Time) error {
	if amount > l.max {
		return &channel.AmountError{Type: typ, Max: l.max}
	}
	return nil
}

func route(id int64, weight int, problem string) Route {
	return Route{Type: "alipay", Row: store.Channel{ID: id}, Weight: weight, Problem: problem}
}

// fixRand 让 Pick 用给定的随机数，测试结束后换回来。
func fixRand(t *testing.T, f func(int) int) {
	old := intN
	intN = f
	t.Cleanup(func() { intN = old })
}

func TestPickByWeight(t *testing.T) {
	m := Method{Type: "alipay", Routes: []Route{route(1, 1, ""), route(2, 3, ""), route(3, 5, "渠道已停用")}}
	// 能收款的是 1 号（权重 1）和 2 号（权重 3），随机数落在 [0,1) 挑 1 号，[1,4) 挑 2 号；停用的 3 号不参与。
	for n, want := range map[int]int64{0: 1, 1: 2, 3: 2} {
		fixRand(t, func(total int) int {
			if total != 4 {
				t.Errorf("权重合计 %d，应为 4", total)
			}
			return n
		})
		if r, err := m.Pick(100, time.Now()); err != nil || r.Row.ID != want {
			t.Errorf("随机数 %d 挑到 %d（%v），应为 %d", n, r.Row.ID, err, want)
		}
	}
}

func TestPickSkipsOverLimit(t *testing.T) {
	fixRand(t, func(int) int { return 0 })
	small := route(1, 1, "")
	small.Channel = limited{max: 50}
	m := Method{Type: "alipay", Routes: []Route{small, route(2, 1, "")}}
	if r, err := m.Pick(100, time.Now()); err != nil || r.Row.ID != 2 {
		t.Errorf("超出 1 号的限额时应挑 2 号：%d %v", r.Row.ID, err)
	}

	// 能收款的都超出限额：返回限额错误。收不了款的渠道不算。
	m = Method{Type: "alipay", Routes: []Route{small, route(2, 1, "渠道已删除")}}
	var ae *channel.AmountError
	if _, err := m.Pick(100, time.Now()); !errors.As(err, &ae) || ae.Max != 50 {
		t.Errorf("都超出限额时：%v", err)
	}
	if err := m.CheckAmount(100, time.Now()); !errors.As(err, &ae) {
		t.Errorf("CheckAmount：%v", err)
	}
	if err := m.CheckAmount(10, time.Now()); err != nil {
		t.Errorf("没超出时 CheckAmount：%v", err)
	}

	// 都收不了款
	m = Method{Type: "alipay", Routes: []Route{route(1, 1, "渠道已停用"), route(2, 1, "渠道已删除")}}
	if _, err := m.Pick(100, time.Now()); !errors.Is(err, ErrUnavailable) || m.Ready() || m.Problem() != "渠道已停用；渠道已删除" {
		t.Errorf("都收不了款时：%v %q", err, m.Problem())
	}
}
