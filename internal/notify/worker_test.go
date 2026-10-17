package notify_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"tillbox/internal/apptest"
	"tillbox/internal/notify"
	"tillbox/internal/store"
)

// paidOrder 下一单并付款，返回平台订单号。通知还没发。
func paidOrder(t *testing.T, e *apptest.Env, notifyURL string) *store.Order {
	t.Helper()
	m := e.NewMerchant(nil)
	res := e.PostForm("/mapi.php", m.SignMD5(map[string]string{
		"type": "alipay", "out_trade_no": "N" + strconv.FormatInt(time.Now().UnixNano(), 10), "notify_url": notifyURL,
		"name": "x", "money": "1", "clientip": "1.1.1.1",
	})).JSON(t)
	tn, _ := res["trade_no"].(string)
	if tn == "" {
		t.Fatalf("下单失败：%v", res)
	}
	e.Pay(tn)
	return e.Order(tn)
}

func load(t *testing.T, e *apptest.Env, orderID int64) store.Notification {
	t.Helper()
	var n store.Notification
	if err := e.DB().NewSelect().Model(&n).Where("order_id = ?", orderID).Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRetrySchedule(t *testing.T) {
	e := apptest.New(t)
	e.Hook.Reply(http.StatusOK, "fail")
	o := paidOrder(t, e, e.Hook.URL+"/n")
	w := e.App.Notifier
	start := time.Now()
	now := start
	w.Now = func() time.Time { return now }

	for i := range notify.Schedule {
		n := load(t, e, o.ID)
		if n.Status != store.NotifyPending {
			t.Fatalf("第 %d 次前状态应是待发：%+v", i+1, n)
		}
		now = store.Time(n.NextAt)
		e.Deliver()
		n = load(t, e, o.ID)
		if n.Attempts != i+1 {
			t.Fatalf("第 %d 次后 attempts=%d", i+1, n.Attempts)
		}
		if i+1 < len(notify.Schedule) {
			want := store.Time(n.FirstAt).Add(notify.Schedule[i+1])
			if !store.Time(n.NextAt).Equal(want) {
				t.Errorf("第 %d 次后下次时间 %v，应为 %v", i+1, store.Time(n.NextAt), want)
			}
			// 没到点不会发。
			e.Deliver()
			if load(t, e, o.ID).Attempts != i+1 {
				t.Error("没到点就发了")
			}
		}
	}
	n := load(t, e, o.ID)
	if n.Status != store.NotifyDead || n.LastError == "" {
		t.Errorf("重试用完应进死信：%+v", n)
	}
	if got := len(e.Hook.Got()); got != len(notify.Schedule) {
		t.Errorf("应发 %d 次，实际 %d", len(notify.Schedule), got)
	}
	if total := store.Time(n.LastAt).Sub(store.Time(n.FirstAt)); total != 24*time.Hour {
		t.Errorf("最后一次应在首次之后 24 小时，实际 %v", total)
	}

	// 手动重发成功后变成已送达，次数不变。
	e.Hook.Reply(http.StatusOK, "success")
	a, err := w.Resend(e.Ctx, o.ID)
	if err != nil || !a.Success || !a.Manual {
		t.Fatalf("手动重发：%+v %v", a, err)
	}
	n = load(t, e, o.ID)
	if n.Status != store.NotifySuccess || n.Attempts != len(notify.Schedule) {
		t.Errorf("手动重发后：%+v", n)
	}
	var attempts []store.NotifyAttempt
	if err := e.DB().NewSelect().Model(&attempts).Where("order_id = ?", o.ID).Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != len(notify.Schedule)+1 {
		t.Errorf("每次投递都要留记录：%d", len(attempts))
	}
}

func TestCatchUpAfterDowntime(t *testing.T) {
	e := apptest.New(t)
	e.Hook.Reply(http.StatusOK, "no")
	o := paidOrder(t, e, e.Hook.URL+"/n")
	w := e.App.Notifier
	now := time.Now()
	w.Now = func() time.Time { return now }
	e.Deliver()
	// 停机 3 小时后回来：错过的时间点不连发，下一次至少隔 30 秒。
	now = now.Add(3 * time.Hour)
	e.Deliver()
	n := load(t, e, o.ID)
	if d := store.Time(n.NextAt).Sub(now); n.Attempts != 2 || d < 30*time.Second-time.Millisecond || d > 30*time.Second {
		t.Errorf("补发节奏不对：attempts=%d next=%v", n.Attempts, d)
	}
}

func TestSuccessDetection(t *testing.T) {
	cases := []struct {
		status int
		body   string
		ok     bool
	}{
		{200, "success", true},
		{200, "SUCCESS", true},
		{200, "Success", true},
		{200, `{"result":"success"}`, true},
		{500, "success", true}, // 官方只看正文
		{200, "succ", false},
		{200, "sUccess", false},
		{200, "", false},
		{404, "not found", false},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.status)+" "+c.body, func(t *testing.T) {
			e := apptest.New(t)
			e.Hook.Reply(c.status, c.body)
			o := paidOrder(t, e, e.Hook.URL+"/n")
			e.Deliver()
			n := load(t, e, o.ID)
			if (n.Status == store.NotifySuccess) != c.ok {
				t.Errorf("状态 %s，期望成功=%v", n.Status, c.ok)
			}
		})
	}
}

func TestNoRedirectAndStopWhenUnpaid(t *testing.T) {
	e := apptest.New(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("success"))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	o := paidOrder(t, e, redirect.URL+"/n")
	e.Deliver()
	if n := load(t, e, o.ID); n.Status != store.NotifyPending || n.Attempts != 1 {
		t.Errorf("跟随了重定向：%+v", n)
	}

	// 订单被撤销关联后，队列停止通知。
	if err := e.App.Ledger.Unlink(e.Ctx, o.ID, "admin", "测试"); err != nil {
		t.Fatal(err)
	}
	if n := load(t, e, o.ID); n.Status != store.NotifyDead {
		t.Errorf("撤销后应停止：%+v", n)
	}
	if _, err := e.App.Notifier.Resend(e.Ctx, o.ID); err == nil {
		t.Error("未支付的订单不该能手动重发")
	}
}

func TestUnreachable(t *testing.T) {
	e := apptest.New(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	o := paidOrder(t, e, url+"/n")
	e.Deliver()
	n := load(t, e, o.ID)
	if n.Status != store.NotifyPending || n.LastError == "" || n.Attempts != 1 {
		t.Errorf("连不上时应记错误、等下次：%+v", n)
	}
}
