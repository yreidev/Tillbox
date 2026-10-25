package admin_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"tillbox/internal/apptest"
	"tillbox/internal/channel/alipayqr"
	"tillbox/internal/epay"
	"tillbox/internal/merchant"
	"tillbox/internal/store"
)

type channelItem struct {
	store.Channel
	Key          string         `json:"key"`
	Config       map[string]any `json:"config"`
	Problem      string         `json:"problem"`
	PayTypes     []string       `json:"pay_types"`
	Orders       int64          `json:"orders"`
	Transactions int64          `json:"transactions"`
	Merchants    int64          `json:"merchants"`
	DeleteBlock  string         `json:"delete_block"`
	Ledger       map[string]any `json:"ledger"`
}

func channels(t *testing.T, a *apptest.Admin) map[int64]channelItem {
	t.Helper()
	l := decode[struct {
		Items []channelItem `json:"items"`
		Demo  bool          `json:"demo"`
	}](t, a.Call("GET", "/web-api/admin/channels", nil))
	if !l.Demo {
		t.Error("测试环境是演示模式")
	}
	out := map[int64]channelItem{}
	for _, c := range l.Items {
		out[c.ID] = c
	}
	return out
}

func TestChannelsAPI(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	demoID := e.Demo.Row().ID
	cs := channels(t, a)
	if d := cs[demoID]; len(cs) != 1 || d.Type != store.ChannelDemo || !d.Enabled || d.Problem != "" || d.Ledger == nil || d.Key != e.ChannelKey() {
		t.Fatalf("初始只有自动建的演示渠道：%+v", cs)
	}

	// 经营码配置校验。
	_, aliPub, _ := epay.GenerateKeyPair()
	ok := alipayqr.Input{AppID: "2021000000000001", AlipayPublicKey: aliPub, QRCodeURL: "https://qr.alipay.com/fkx12345"}
	create := func(in alipayqr.Input, enabled bool) *apptest.Response {
		return a.Call("POST", "/web-api/admin/channels", map[string]any{"type": store.ChannelAlipayQR, "name": "经营码 A", "enabled": enabled, "config": in})
	}
	for _, c := range []struct {
		edit    func(*alipayqr.Input)
		enabled bool
		want    string
	}{
		{func(x *alipayqr.Input) { x.Gateway = "ftp://x" }, false, "网关地址"},
		{func(x *alipayqr.Input) { x.AppID = "abc" }, false, "一串数字"},
		{func(x *alipayqr.Input) { x.AlipayPublicKey = "xyz" }, false, "支付宝公钥格式不对"},
		{func(x *alipayqr.Input) { x.QRCodeURL = "https://evil.example.com/fkx" }, false, "qr.alipay.com"},
		{func(x *alipayqr.Input) { x.QRCodeURL = "http://qr.alipay.com/fkx" }, false, "qr.alipay.com"},
		{func(x *alipayqr.Input) { x.PayeeName = strings.Repeat("名", 33) }, false, "32"},
		{func(x *alipayqr.Input) { x.AppPrivateKey = "bad" }, false, "应用私钥格式不对"},
		{func(*alipayqr.Input) {}, true, "启用前要填完整"},
	} {
		in := ok
		c.edit(&in)
		expect(t, create(in, c.enabled), http.StatusBadRequest, c.want)
	}
	expect(t, a.Call("POST", "/web-api/admin/channels", map[string]any{"type": "wechat", "name": "x"}), http.StatusBadRequest, "不支持的渠道类型")
	expect(t, a.Call("POST", "/web-api/admin/channels", map[string]any{"type": store.ChannelAlipayQR, "name": " "}), http.StatusBadRequest, "渠道名称")

	// 公钥里的换行和空格会被去掉；网关留空用正式环境。
	spaced := ok
	spaced.AlipayPublicKey = aliPub[:40] + "\n " + aliPub[40:]
	res := decode[map[string]store.Channel](t, create(spaced, false))
	ali := res["channel"]
	v := channels(t, a)[ali.ID]
	if v.Config["alipay_public_key"] != aliPub || v.Config["gateway"] != alipayqr.GatewayProd || v.Config["app_id"] != ok.AppID || v.Config["has_private_key"] != false || v.Enabled {
		t.Errorf("经营码配置：%+v", v)
	}
	if !strings.Contains(v.Problem, "停用") || v.Ledger == nil || len(v.PayTypes) != 1 || v.PayTypes[0] != "alipay" {
		t.Errorf("停用的经营码渠道：%+v", v)
	}
	// 测试连接：还没有应用密钥。
	r := decode[map[string]any](t, a.Call("POST", fmt.Sprintf("/web-api/admin/channels/%d/test", ali.ID), nil))
	if r["ok"] != false || !strings.Contains(r["message"].(string), "还没有填完") {
		t.Errorf("测试连接：%v", r)
	}
	r = decode[map[string]any](t, a.Call("POST", fmt.Sprintf("/web-api/admin/channels/%d/test", demoID), nil))
	if r["ok"] != true {
		t.Errorf("演示渠道测试连接：%v", r)
	}

	// 生成应用密钥对：返回公钥，私钥加密存库。
	kp := decode[map[string]any](t, a.Call("POST", fmt.Sprintf("/web-api/admin/channels/%d/keypair", ali.ID), nil))
	v = channels(t, a)[ali.ID]
	if kp["public_key"] == "" || v.Config["app_public_key"] != kp["public_key"] || v.Config["has_private_key"] != true {
		t.Errorf("生成密钥对：%v %+v", kp, v.Config)
	}
	expect(t, a.Call("POST", fmt.Sprintf("/web-api/admin/channels/%d/keypair", demoID), nil), http.StatusBadRequest, "不需要密钥对")
	var row store.Channel
	if err := e.DB().NewSelect().Model(&row).Where("id = ?", ali.ID).Scan(e.Ctx); err != nil {
		t.Fatal(err)
	}
	// 明文私钥是 base64 的 DER，以 MII 开头；密文是随机 base64，可能在中间碰巧出现 MII，只看开头。
	if stored, err := alipayqr.ParseConfig(row.Config); err != nil || stored.AppPrivateKey == "" || strings.HasPrefix(stored.AppPrivateKey, "MII") {
		t.Errorf("应用私钥应加密存库：%v", err)
	}
	// 导入已有私钥并启用：公钥按私钥算出来。
	priv, pub, _ := epay.GenerateKeyPair()
	imp := ok
	imp.AppPrivateKey = priv
	update := func(id int64, name string, enabled bool, in any) *apptest.Response {
		return a.Call("PUT", fmt.Sprintf("/web-api/admin/channels/%d", id), map[string]any{"name": name, "enabled": enabled, "config": in})
	}
	expect(t, update(ali.ID, "经营码 A", true, imp), http.StatusOK, "")
	if v = channels(t, a)[ali.ID]; v.Config["app_public_key"] != pub || !v.Enabled || v.Problem != "" {
		t.Errorf("导入私钥后：%+v", v)
	}
	// 不传私钥时保留原来的。
	expect(t, update(ali.ID, "经营码 B", true, ok), http.StatusOK, "")
	if v = channels(t, a)[ali.ID]; v.Config["app_public_key"] != pub || v.Name != "经营码 B" {
		t.Error("没传私钥时不该改动应用密钥")
	}
	if body := string(a.Call("GET", "/web-api/admin/channels", nil).Body); strings.Contains(body, "PRIVATE") || strings.Contains(body, `"app_private_key"`) {
		t.Error("渠道列表不该带出私钥")
	}
	expect(t, update(999, "x", false, ok), http.StatusNotFound, "渠道不存在")
	// 同一个应用 ID 或同一张经营码不能配成两个经营码渠道；本渠道自己保存不算重复。
	other := ok
	other.QRCodeURL = "https://qr.alipay.com/fkx67890"
	expect(t, create(other, false), http.StatusBadRequest, "应用 ID 已经用在渠道「经营码 B」上")
	other = ok
	other.AppID = "2021000000000002"
	expect(t, create(other, false), http.StatusBadRequest, "这张经营码已经用在渠道「经营码 B」上")
	other.QRCodeURL = "https://qr.alipay.com/fkx67890"
	second := decode[map[string]store.Channel](t, create(other, false))["channel"]
	expect(t, update(second.ID, "经营码 C", false, ok), http.StatusBadRequest, "已经用在渠道「经营码 B」上")
	expect(t, a.Call("DELETE", fmt.Sprintf("/web-api/admin/channels/%d", second.ID), nil), http.StatusOK, "")

	// 商户按支付方式选渠道：新建默认第一个启用、开放了这种方式的渠道；可以改，不存在的渠道、没开放这种方式的渠道拒绝。
	m := e.NewMerchant(nil)
	if m.Channel != e.ChannelKey() {
		t.Errorf("新商户默认渠道 %s", m.Channel)
	}
	alipay := func(id int64) map[string]int64 { return map[string]int64{"alipay": id} }
	mpath := fmt.Sprintf("/web-api/admin/merchants/%d", m.ID)
	expect(t, a.Call("PUT", mpath, merchant.Input{Name: "x", Enabled: true, Routes: alipay(999)}), http.StatusBadRequest, "渠道不存在")
	expect(t, a.Call("PUT", mpath, merchant.Input{Name: "x", Enabled: true, Routes: map[string]int64{"wxpay": ali.ID}}), http.StatusBadRequest, "没有开放微信支付")
	expect(t, a.Call("PUT", mpath, merchant.Input{Name: "x", Enabled: true, Routes: map[string]int64{"nope": ali.ID}}), http.StatusBadRequest, "不认识的支付方式")
	expect(t, a.Call("PUT", mpath, merchant.Input{Name: "x", Enabled: true, Routes: alipay(ali.ID)}), http.StatusOK, "")
	// 没传收款方式时保留原样。
	expect(t, a.Call("PUT", mpath, merchant.Input{Name: "x", Enabled: true}), http.StatusOK, "")
	if got, _ := e.App.Merchants.Get(e.Ctx, m.ID); got.Routes["alipay"] != ali.ID || len(got.Routes) != 1 {
		t.Errorf("商户的收款方式应为支付宝走 %d，实际 %v", ali.ID, got.Routes)
	}
	if v = channels(t, a)[ali.ID]; v.Merchants != 1 {
		t.Errorf("渠道的商户数：%+v", v)
	}

	// 停用商户支付宝走的渠道后，下单报通道不可用；换回演示渠道后可以下单，订单记在演示渠道名下。
	expect(t, update(ali.ID, "经营码 B", false, ok), http.StatusOK, "")
	res2 := e.PostForm("/mapi.php", m.SignMD5(map[string]string{"type": "alipay", "out_trade_no": "C1", "notify_url": e.Hook.URL + "/n", "name": "x", "money": "1", "clientip": "1.1.1.1"})).JSON(t)
	if fmt.Sprint(res2["code"]) != "-2" || !strings.Contains(fmt.Sprint(res2["msg"]), "支付通道暂时不可用") {
		t.Errorf("渠道停用时下单：%v", res2)
	}
	expect(t, a.Call("PUT", mpath, merchant.Input{Name: "x", Enabled: true, Routes: alipay(demoID)}), http.StatusOK, "")
	tn := order(t, e, m, "C2", "1", "x")
	if o := e.Order(tn); o.Channel != e.ChannelKey() {
		t.Errorf("订单渠道 %s", o.Channel)
	}

	// 删除：有订单、有流水、有商户在用的渠道不能删，空渠道可以删。列表给出的原因和删除接口一致。
	blocked := func(id int64, want string) {
		t.Helper()
		got := channels(t, a)[id].DeleteBlock
		if (want == "" && got != "") || !strings.Contains(got, want) {
			t.Errorf("渠道 %d 不能删除的原因 %q，应含 %q", id, got, want)
		}
		if want != "" {
			expect(t, a.Call("DELETE", fmt.Sprintf("/web-api/admin/channels/%d", id), nil), http.StatusBadRequest, want)
		}
	}
	blocked(demoID, "已经有订单")
	expect(t, a.Call("PUT", mpath, merchant.Input{Name: "x", Enabled: true, Routes: alipay(ali.ID)}), http.StatusOK, "")
	blocked(ali.ID, "还有 1 个商户在用")
	expect(t, a.Call("PUT", mpath, merchant.Input{Name: "x", Enabled: true, Routes: alipay(demoID)}), http.StatusOK, "")
	blocked(ali.ID, "")
	txn := &store.Transaction{Channel: ali.Key(), ExternalID: "T1", Direction: store.DirIn, Amount: 100, State: store.TxnNew}
	if _, err := e.DB().NewInsert().Model(txn).Exec(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if v = channels(t, a)[ali.ID]; v.Transactions != 1 || v.Orders != 0 {
		t.Errorf("只有流水的渠道：%+v", v)
	}
	blocked(ali.ID, "已经有流水")
	if _, err := e.DB().NewDelete().Model(txn).WherePK().Exec(e.Ctx); err != nil {
		t.Fatal(err)
	}
	expect(t, a.Call("DELETE", fmt.Sprintf("/web-api/admin/channels/%d", ali.ID), nil), http.StatusOK, "")
	if cs = channels(t, a); len(cs) != 1 {
		t.Errorf("删除后应只剩演示渠道：%+v", cs)
	}
	if len(e.App.Pollers.Keys()) != 1 {
		t.Errorf("删除渠道后应停掉它的查账任务：%v", e.App.Pollers.Keys())
	}
	for _, want := range []string{"channel.create", "channel.update", "channel.keypair", "channel.delete"} {
		if !hasAudit(t, e, want) {
			t.Errorf("没有审计记录 %s", want)
		}
	}
}

// 演示渠道只能在演示模式下新建；经营码渠道各有自己的查账任务。
func TestChannelPollersFollowRegistry(t *testing.T) {
	e := apptest.New(t)
	a := e.Login()
	before := e.App.Pollers.Keys()
	res := decode[map[string]store.Channel](t, a.Call("POST", "/web-api/admin/channels", map[string]any{"type": store.ChannelDemo, "name": "演示 2", "enabled": true}))
	second := res["channel"]
	keys := e.App.Pollers.Keys()
	if len(keys) != len(before)+1 || keys[len(keys)-1] != second.Key() {
		t.Fatalf("新渠道应有自己的查账任务：%v", keys)
	}
	if p := e.App.Pollers.Get(e.ChannelKey()); p == nil || p != e.App.Pollers.All()[0] {
		t.Error("原有的查账任务应保留")
	}
}
