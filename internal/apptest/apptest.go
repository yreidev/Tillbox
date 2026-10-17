// Package apptest 起一个完整的 Tillbox（假渠道 + 临时数据库）给集成测试用。
//
// 默认用临时目录里的 SQLite。设置了 TILLBOX_TEST_PG_DSN（例如 postgres://u:p@127.0.0.1:5432/db?sslmode=disable）时
// 改用 PostgreSQL：每个测试建一个独立的 schema，结束后删掉。
package apptest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/app"
	"tillbox/internal/channel/fake"
	"tillbox/internal/config"
	"tillbox/internal/epay"
	"tillbox/internal/merchant"
	"tillbox/internal/secret"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// Env 是一套跑起来的服务。
type Env struct {
	T      testing.TB
	Ctx    context.Context
	App    *app.App
	Server *httptest.Server
	URL    string
	Hook   *Receiver
	// Driver 是 sqlite 或 postgres。
	Driver string
	// Demo 是启动时自动建的演示渠道，新建商户默认用它。
	Demo *fake.Channel
}

// ChannelKey 是演示渠道的实例键（订单和流水的 channel 列）。
func (e *Env) ChannelKey() string { return e.Demo.Key() }

// New 起一套服务。查账的"现在"被往后拨了一个安全延迟，测试里付完款马上 Poll 就能查到。
func New(t testing.TB) *Env {
	t.Helper()
	ctx := context.Background()
	driver, dsn := "sqlite", filepath.Join(t.TempDir(), "tillbox.db")
	if pg := os.Getenv("TILLBOX_TEST_PG_DSN"); pg != "" {
		driver, dsn = "postgres", pgSchema(t, pg)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{Demo: true, DBDriver: driver, DBDSN: dsn, DataDir: t.TempDir()}
	a, err := app.New(ctx, cfg, db, box, fstest.MapFS{})
	if err != nil {
		t.Fatal(err)
	}
	safety := time.Duration(settings.DefaultPayment().SafetyDelaySecond+1) * time.Second
	a.Pollers.SetNow(func() time.Time { return time.Now().Add(safety) })
	demos := a.Channels.Demos()
	if len(demos) != 1 {
		t.Fatalf("演示模式应自动建一个演示渠道，实际 %d 个", len(demos))
	}

	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	e := &Env{T: t, Ctx: ctx, App: a, Server: srv, URL: srv.URL, Driver: driver, Demo: demos[0]}
	e.Hook = NewReceiver(t)
	return e
}

// pgSchema 在库里建一个随机 schema，返回指向它的连接串。
func pgSchema(t testing.TB, dsn string) string {
	t.Helper()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "t_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func (e *Env) DB() *bun.DB { return e.App.DB }

// Merchant 是测试用的商户。
type Merchant struct {
	ID      int64
	Key     string
	Priv    *rsa.PrivateKey // RSA 商户才有
	Channel string          // 商户所用渠道的实例键
}

// PID 返回字符串形式的商户号。
func (m *Merchant) PID() string { return strconv.FormatInt(m.ID, 10) }

// SignMD5 给参数加上 MD5 签名，按常见调用方和官方 V1 SDK 的做法：只跳过空串。
func (m *Merchant) SignMD5(p map[string]string) url.Values {
	q := epay.Params{}
	for k, v := range p {
		q[k] = v
	}
	if _, ok := q["pid"]; !ok {
		q["pid"] = m.PID()
	}
	q["sign"] = epay.MD5Sign(q, m.Key)
	if _, ok := q["sign_type"]; !ok {
		q["sign_type"] = "MD5"
	}
	return values(q)
}

// SignRSA 按官方 V2 SDK 的做法签名：补上 pid 和当前时间戳，用商户私钥签。
func (m *Merchant) SignRSA(p map[string]string) url.Values {
	q := epay.Params{}
	for k, v := range p {
		q[k] = v
	}
	if _, ok := q["pid"]; !ok {
		q["pid"] = m.PID()
	}
	if _, ok := q["timestamp"]; !ok {
		q["timestamp"] = strconv.FormatInt(time.Now().Unix(), 10)
	}
	sign, err := epay.RSASign(epay.SignContent(q), m.Priv)
	if err != nil {
		panic(err)
	}
	q["sign"] = sign
	q["sign_type"] = "RSA"
	return values(q)
}

func values(p epay.Params) url.Values {
	v := url.Values{}
	for k, s := range p {
		v.Set(k, s)
	}
	return v
}

// NewMerchant 建一个商户。in 为空时用默认值（启用、不限域名）。
func (e *Env) NewMerchant(in *merchant.Input) *Merchant {
	e.T.Helper()
	if in == nil {
		in = &merchant.Input{Name: "测试商户", Enabled: true}
	}
	m, key, err := e.App.Merchants.Create(e.Ctx, *in)
	if err != nil {
		e.T.Fatal(err)
	}
	return &Merchant{ID: m.ID, Key: key, Channel: store.ChannelKey(m.ChannelID)}
}

// NewRSAMerchant 建一个带 RSA 公钥的商户。rsaOnly 为真时只收 RSA 签名。
func (e *Env) NewRSAMerchant(rsaOnly bool) *Merchant {
	e.T.Helper()
	priv, pub, err := epay.GenerateKeyPair()
	if err != nil {
		e.T.Fatal(err)
	}
	in := merchant.Input{Name: "RSA 商户", Enabled: true, RSAPublicKey: pub}
	if rsaOnly {
		in.KeyType = store.KeyTypeRSAOnly
	}
	m := e.NewMerchant(&in)
	m.Priv, err = epay.ParsePrivateKey(priv)
	if err != nil {
		e.T.Fatal(err)
	}
	return m
}

// PlatformKey 返回平台公钥（验 V2 响应和通知用）。
func (e *Env) PlatformKey() *rsa.PublicKey {
	e.T.Helper()
	p, err := e.App.Settings.Platform(e.Ctx)
	if err != nil {
		e.T.Fatal(err)
	}
	k, err := epay.ParsePublicKey(p.PublicKey)
	if err != nil {
		e.T.Fatal(err)
	}
	return k
}

// Response 是一次请求的结果。
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON 把响应体解析成 map，数字保留成 json.Number。
func (r *Response) JSON(t testing.TB) map[string]any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(r.Body))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		t.Fatalf("响应不是 JSON：%v\n%s", err, r.Body)
	}
	return m
}

var noRedirect = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Do 发一个请求，不跟随跳转。
func (e *Env) Do(req *http.Request) *Response {
	e.T.Helper()
	resp, err := noRedirect.Do(req)
	if err != nil {
		e.T.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		e.T.Fatal(err)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: body}
}

// PostForm 发 application/x-www-form-urlencoded 表单。path 可以带查询串。
func (e *Env) PostForm(path string, form url.Values) *Response {
	e.T.Helper()
	req, err := http.NewRequest(http.MethodPost, e.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		e.T.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.Do(req)
}

// Get 发 GET 请求。path 可以带查询串。
func (e *Env) Get(path string) *Response {
	e.T.Helper()
	req, err := http.NewRequest(http.MethodGet, e.URL+path, nil)
	if err != nil {
		e.T.Fatal(err)
	}
	return e.Do(req)
}

// Pay 模拟付款人按订单的应付金额付款，然后查一轮账。
func (e *Env) Pay(tradeNo string) {
	e.T.Helper()
	o, err := e.App.Orders.ByTradeNo(e.Ctx, tradeNo)
	if err != nil {
		e.T.Fatal(err)
	}
	e.PayAmount(o.PayAmount)
}

// PayAmount 模拟付一笔指定金额（分），然后查一轮账。
func (e *Env) PayAmount(cents int64) string {
	e.T.Helper()
	id := e.Demo.Pay(cents, time.Now(), "测试付款人(test***@example.com)")
	e.Poll()
	return id
}

// Poll 让每个推断型渠道各查一轮账。
func (e *Env) Poll() {
	e.T.Helper()
	if err := e.App.Pollers.RunOnce(e.Ctx); err != nil {
		e.T.Fatal(err)
	}
}

// Deliver 发送所有到期的通知。
func (e *Env) Deliver() {
	e.T.Helper()
	if err := e.App.Notifier.RunDue(e.Ctx); err != nil {
		e.T.Fatal(err)
	}
}

// Order 读订单。
func (e *Env) Order(tradeNo string) *store.Order {
	e.T.Helper()
	o, err := e.App.Orders.ByTradeNo(e.Ctx, tradeNo)
	if err != nil {
		e.T.Fatal(err)
	}
	return o
}

// Receiver 是商户的通知接收端：记下收到的请求，按设定回复。
type Receiver struct {
	URL    string
	mu     sync.Mutex
	reqs   []Received
	reply  string
	status int
}

// Received 是收到的一次通知。
type Received struct {
	Method string
	Path   string
	Query  url.Values
}

func NewReceiver(t testing.TB) *Receiver {
	r := &Receiver{reply: "success", status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.reqs = append(r.reqs, Received{Method: req.Method, Path: req.URL.Path, Query: req.URL.Query()})
		reply, status := r.reply, r.status
		r.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	r.URL = srv.URL
	return r
}

// Reply 设定之后的回复。
func (r *Receiver) Reply(status int, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status, r.reply = status, body
}

// Got 返回收到的全部通知。
func (r *Receiver) Got() []Received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Received(nil), r.reqs...)
}

// Params 把收到的查询串转成签名参数。
func (rc Received) Params() epay.Params {
	p := epay.Params{}
	for k, v := range rc.Query {
		p[k] = v[len(v)-1]
	}
	return p
}

// Admin 是登录了后台的客户端。
type Admin struct {
	e      *Env
	client *http.Client
}

// Login 用首次启动生成的管理员密码登录。
func (e *Env) Login() *Admin {
	e.T.Helper()
	jar, _ := cookiejar.New(nil)
	a := &Admin{e: e, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	res := a.Call(http.MethodPost, "/web-api/auth/login", map[string]string{"password": e.App.AdminPassword})
	if res.Status != http.StatusOK {
		e.T.Fatalf("登录失败：%d %s", res.Status, res.Body)
	}
	return a
}

// Call 调后台接口，自动带上 CSRF 头。
func (a *Admin) Call(method, path string, body any) *Response {
	a.e.T.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.e.T.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.e.URL+path, r)
	if err != nil {
		a.e.T.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tillbox", "1")
	resp, err := a.client.Do(req)
	if err != nil {
		a.e.T.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		a.e.T.Fatal(err)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: b}
}
