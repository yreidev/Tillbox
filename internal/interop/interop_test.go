//go:build interop

// Package interop 用官方易支付 V1/V2 SDK（PHP）对着本系统跑一遍：下单、跳转、查单、退款、验通知和回跳签名。
//
// 需要 Docker 和官方 SDK 源码，SDK 目录里要有 v1/lib/EpayCore.class.php 和 v2/SDK/lib/EpayCore.class.php：
//
//	TILLBOX_EPAY_SDK=<官方 SDK 目录> go test -tags interop ./internal/interop/
//
// PHP 容器用 --network host 访问测试服务（只监听 127.0.0.1），SDK 和驱动脚本只读挂载。
package interop

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tillbox/internal/apptest"
	"tillbox/internal/epay"
	"tillbox/internal/store"
)

const image = "php:8.3-cli-alpine"

type step map[string]any

type result struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

func (r result) obj(t *testing.T) map[string]any {
	t.Helper()
	if !r.OK {
		t.Fatalf("SDK 报错：%s", r.Error)
	}
	var m map[string]any
	if err := json.Unmarshal(r.Result, &m); err != nil {
		t.Fatalf("SDK 返回的不是对象：%s", r.Result)
	}
	return m
}

func (r result) str(t *testing.T) string {
	t.Helper()
	var s string
	if !r.OK || json.Unmarshal(r.Result, &s) != nil {
		t.Fatalf("SDK 返回：%+v", r)
	}
	return s
}

func (r result) boolean(t *testing.T) bool {
	t.Helper()
	var b bool
	if !r.OK || json.Unmarshal(r.Result, &b) != nil {
		t.Fatalf("SDK 返回：%+v", r)
	}
	return b
}

type runner struct {
	t      *testing.T
	sdk    string
	driver string
	config map[string]any
}

func (r *runner) run(steps ...step) []result {
	r.t.Helper()
	in, _ := json.Marshal(map[string]any{"v1": r.config["v1"], "v2": r.config["v2"], "steps": steps})
	cmd := exec.Command("docker", "run", "--rm", "-i", "--network", "host",
		"-v", r.sdk+":/sdk:ro", "-v", r.driver+":/drv:ro", image,
		"php", "-d", "display_errors=stderr", "-d", "error_reporting=E_ALL&~E_WARNING&~E_DEPRECATED", "/drv/driver.php")
	cmd.Stdin = bytes.NewReader(in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("PHP 容器运行失败：%v\n%s", err, stderr.String())
	}
	var res []result
	if err := json.Unmarshal(out, &res); err != nil || len(res) != len(steps) {
		r.t.Fatalf("PHP 输出不对：%v\n%s\n%s", err, out, stderr.String())
	}
	return res
}

func params(t *testing.T, raw string) map[string]string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p := map[string]string{}
	for k, v := range u.Query() {
		p[k] = v[0]
	}
	return p
}

func TestOfficialSDK(t *testing.T) {
	sdk := os.Getenv("TILLBOX_EPAY_SDK")
	if sdk == "" {
		t.Skip("没设 TILLBOX_EPAY_SDK")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("没有 docker")
	}
	sdk, _ = filepath.Abs(sdk)
	driver, _ := filepath.Abs("../../scripts/interop")

	e := apptest.New(t)
	m := e.NewRSAMerchant(false)
	priv, err := epay.MarshalPrivateKey(m.Priv)
	if err != nil {
		t.Fatal(err)
	}
	plat, err := e.App.Settings.Platform(e.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{t: t, sdk: sdk, driver: driver, config: map[string]any{
		"v1": map[string]string{"apiurl": e.URL + "/", "pid": m.PID(), "key": m.Key},
		"v2": map[string]string{"apiurl": e.URL + "/", "pid": m.PID(), "platform_public_key": plat.PublicKey, "merchant_private_key": priv},
	}}
	order := func(out, money, ver string) map[string]string {
		p := map[string]string{
			"type": "alipay", "out_trade_no": out, "notify_url": e.Hook.URL + "/" + ver + "/notify",
			"return_url": e.Hook.URL + "/" + ver + "/return", "name": "互通测试 & 会员", "money": money,
			"clientip": "192.0.2.10", "device": "pc", "param": "uid=7",
		}
		if ver == "v1" {
			p["pid"] = m.PID() // V1 SDK 不自动带 pid，官方示例由调用方放进参数
		}
		return p
	}

	// 第一轮：下单和跳转链接。
	res := r.run(
		step{"op": "v1.apiPay", "params": order("I1", "1.50", "v1")},
		step{"op": "v1.getPayLink", "params": order("I2", "2", "v1")},
		step{"op": "v2.apiPay", "params": order("I3", "3.3", "v2")},
		step{"op": "v2.getPayLink", "params": order("I4", "4", "v2")},
		step{"op": "v1.pagePay", "params": order("I5", "5", "v1")},
	)
	v1Order := res[0].obj(t)
	if v1Order["code"] != float64(1) || v1Order["trade_no"] == nil || !strings.HasPrefix(v1Order["payurl"].(string), e.URL+"/cashier/") {
		t.Fatalf("V1 apiPay：%v", v1Order)
	}
	v2Order := res[2].obj(t) // SDK 已经验过响应签名
	if v2Order["pay_type"] != "qrcode" || v2Order["trade_no"] == nil {
		t.Fatalf("V2 apiPay：%v", v2Order)
	}
	for i, link := range []string{res[1].str(t), res[3].str(t)} {
		got := e.Get(strings.TrimPrefix(link, e.URL))
		if got.Status != http.StatusFound && got.Status != http.StatusSeeOther || !strings.HasPrefix(got.Header.Get("Location"), e.URL+"/cashier/") {
			t.Errorf("第 %d 个跳转链接：%d %s", i+1, got.Status, got.Body)
		}
	}
	if form := res[4].str(t); !strings.Contains(form, `action="`+e.URL+`/submit.php"`) {
		t.Errorf("pagePay 表单：%s", form)
	}
	tn1, tn3 := v1Order["trade_no"].(string), v2Order["trade_no"].(string)
	if e.Order(tn1).Version != 1 || e.Order(tn3).Version != 2 || e.Order(tn1).Money != "1.50" {
		t.Fatalf("订单版本或金额不对")
	}

	// 付款、发通知、拿回跳地址。
	e.Pay(tn1)
	e.Pay(tn3)
	e.Deliver()
	notifies := map[string]map[string]string{}
	for _, n := range e.Hook.Got() {
		notifies[n.Path] = n.Params()
	}
	n1, n3 := notifies["/v1/notify"], notifies["/v2/notify"]
	if n1 == nil || n3 == nil {
		t.Fatalf("通知没收到：%v", notifies)
	}
	ret := func(tn string) map[string]string {
		u, _ := e.Get("/web-api/cashier/" + tn + "/return").JSON(t)["url"].(string)
		return params(t, u)
	}
	r1, r3 := ret(tn1), ret(tn3)
	tampered := func(p map[string]string) map[string]string {
		c := map[string]string{}
		for k, v := range p {
			c[k] = v
		}
		c["money"] = "999"
		return c
	}

	// 第二轮：查单、退款、验签。
	res = r.run(
		step{"op": "v1.queryOrder", "trade_no": tn1},
		step{"op": "v1.orderStatus", "trade_no": tn1},
		step{"op": "v1.verifyNotify", "params": n1},
		step{"op": "v1.verifyReturn", "params": r1},
		step{"op": "v1.verifyNotify", "params": tampered(n1)},
		step{"op": "v1.refund", "trade_no": tn1, "money": "1.50"},
		step{"op": "v2.queryOrder", "trade_no": tn3},
		step{"op": "v2.orderStatus", "trade_no": tn3},
		step{"op": "v2.verify", "params": n3},
		step{"op": "v2.verify", "params": r3},
		step{"op": "v2.verify", "params": tampered(n3)},
		step{"op": "v2.refund", "out_refund_no": "R1", "trade_no": tn3, "money": "3.3"},
		step{"op": "v2.execute", "path": "api/merchant/info", "params": map[string]string{}},
		step{"op": "v2.execute", "path": "/api/merchant/orders", "params": map[string]string{"offset": "0", "limit": "10"}},
		step{"op": "v2.execute", "path": "api/pay/close", "params": map[string]string{"out_trade_no": "I4"}},
		step{"op": "v2.queryOrder", "trade_no": "1234567890123456789"},
	)
	q1 := res[0].obj(t)
	if q1["code"] != float64(1) || q1["status"] != float64(1) && q1["status"] != "1" || q1["money"] != "1.50" || q1["out_trade_no"] != "I1" {
		t.Errorf("V1 查单：%v", q1)
	}
	if !res[1].boolean(t) {
		t.Error("V1 orderStatus 应为已支付")
	}
	if !res[2].boolean(t) || !res[3].boolean(t) {
		t.Errorf("官方 V1 SDK 验通知/回跳签名失败：%v %v", n1, r1)
	}
	if res[4].boolean(t) {
		t.Error("改过金额的 V1 通知不该验过")
	}
	if rf := res[5].obj(t); rf["code"] == float64(0) || rf["msg"] == nil {
		t.Errorf("V1 退款应失败并给出原因：%v", rf)
	}
	q3 := res[6].obj(t)
	if q3["status"] != float64(1) || q3["money"] != "3.30" || q3["buyer"] == nil {
		t.Errorf("V2 查单：%v", q3)
	}
	if !res[7].boolean(t) {
		t.Error("V2 orderStatus 应为已支付")
	}
	if !res[8].boolean(t) || !res[9].boolean(t) {
		t.Errorf("官方 V2 SDK 验通知/回跳签名失败：%v %v", n3, r3)
	}
	if res[10].boolean(t) {
		t.Error("改过金额的 V2 通知不该验过")
	}
	if res[11].OK || !strings.Contains(res[11].Error, "不支持API退款") {
		t.Errorf("V2 退款：%+v", res[11])
	}
	if info := res[12].obj(t); info["pid"] != float64(m.ID) && info["pid"] != m.PID() {
		t.Errorf("V2 商户信息：%v", info)
	}
	if list := res[13].obj(t); list["code"] != float64(0) {
		t.Errorf("V2 订单列表：%v", list)
	}
	if cl := res[14].obj(t); cl["code"] != float64(0) {
		t.Errorf("V2 关单：%v", cl)
	}
	if res[15].OK {
		t.Errorf("查不存在的订单应抛错：%+v", res[15])
	}
	var closed store.Order
	if err := e.DB().NewSelect().Model(&closed).Where("out_trade_no = ?", "I4").Scan(e.Ctx); err != nil || closed.Status != store.OrderClosed {
		t.Errorf("I4 应已关闭：%s %v", closed.Status, err)
	}
}
