package monitor

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/uptrace/bun"

	"tillbox/internal/settings"
	"tillbox/internal/store"
)

const (
	// BindTTL 是绑定链接的有效期。只在绑定期间收消息（getUpdates），平时不占用机器人的消息接收，
	// 沿用别的程序也在收消息的机器人时不会互相抢。
	BindTTL     = 10 * time.Minute
	pollTimeout = 30 * time.Second
	sendTimeout = 15 * time.Second
	// maxText 留在 Telegram 单条消息 4096 字的上限以内。
	maxText = 3500
)

var (
	ErrNoToken = errors.New("还没有填 Telegram 机器人 Token")
	ErrNoChats = errors.New("还没有绑定接收告警的 Telegram 账号")
)

// Binding 是正在进行的绑定：管理员打开 Link，在 Telegram 里点 Start，机器人收到“/start 验证码”就记下这个会话。
type Binding struct {
	Link      string `json:"link"`
	ExpiresAt int64  `json:"expires_at"`
	Error     string `json:"error"` // 收消息出错（例如这个机器人设了 Webhook）
	code      string
}

// Telegram 用机器人给绑定的单聊发告警。
type Telegram struct {
	DB       *bun.DB
	Settings *settings.Service
	// ServerURL 是 Bot API 地址，测试里换成假服务器。
	ServerURL string
	Client    *http.Client
	// Now 可以在测试里替换。
	Now func() time.Time

	mu      sync.Mutex
	base    context.Context // 绑定时收消息的任务跟着它结束，由 Service.Run 设置
	binding *Binding
	stop    context.CancelFunc
	done    chan struct{}
}

func NewTelegram(db *bun.DB, st *settings.Service) *Telegram {
	return &Telegram{
		DB: db, Settings: st, ServerURL: "https://api.telegram.org", Now: time.Now,
		Client: &http.Client{Timeout: pollTimeout + 5*time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (t *Telegram) bot(token string, opts ...bot.Option) (*bot.Bot, error) {
	opts = append([]bot.Option{bot.WithServerURL(t.ServerURL), bot.WithHTTPClient(pollTimeout, t.Client), bot.WithSkipGetMe()}, opts...)
	return bot.New(token, opts...)
}

// clean 去掉错误信息里可能带出的 Token。
func clean(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "***"))
}

// Configure 校验 Token（调 getMe）并保存。token 为空时只改提醒间隔。换成另一个机器人时，
// 原来绑定的会话收不到新机器人的消息，一起清掉。
func (t *Telegram) Configure(ctx context.Context, token string, remindHours int) (settings.Telegram, error) {
	cur, err := t.Settings.Telegram(ctx)
	if err != nil {
		return cur, err
	}
	next := cur
	next.RemindHours = remindHours
	token = strings.TrimSpace(token)
	if token != "" {
		b, err := t.bot(token)
		if err != nil {
			return cur, fmt.Errorf("机器人 Token 格式不对：%w", clean(err, token))
		}
		cctx, cancel := context.WithTimeout(ctx, sendTimeout)
		me, err := b.GetMe(cctx)
		cancel()
		if err != nil {
			if errors.Is(err, bot.ErrorUnauthorized) || errors.Is(err, bot.ErrorNotFound) {
				return cur, errors.New("Telegram 不认这个 Token，请从 BotFather 重新复制")
			}
			return cur, fmt.Errorf("连不上 Telegram：%w", clean(err, token))
		}
		next.Token, next.BotID, next.BotUsername = token, me.ID, me.Username
	}
	if err := t.Settings.SaveTelegram(ctx, next); err != nil {
		return cur, err
	}
	if cur.BotID != 0 && next.BotID != cur.BotID {
		t.CancelBind()
		if _, err := t.DB.NewDelete().Model((*store.TelegramChat)(nil)).Where("1 = 1").Exec(ctx); err != nil {
			return next, err
		}
	}
	return next, nil
}

// Clear 删掉 Token 和全部绑定，不再发告警。
func (t *Telegram) Clear(ctx context.Context) error {
	t.CancelBind()
	if err := t.Settings.SaveTelegram(ctx, settings.Telegram{RemindHours: 1}); err != nil {
		return err
	}
	_, err := t.DB.NewDelete().Model((*store.TelegramChat)(nil)).Where("1 = 1").Exec(ctx)
	return err
}

func (t *Telegram) setBase(ctx context.Context) {
	t.mu.Lock()
	t.base = ctx
	t.mu.Unlock()
}

// randomCode 是 Telegram /start 参数允许的字符（A-Z、a-z、0-9）组成的验证码。
func randomCode() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// Bind 生成绑定链接，并在有效期内收机器人的消息。再点一次会作废上一个链接。
func (t *Telegram) Bind(ctx context.Context) (Binding, error) {
	cfg, err := t.Settings.Telegram(ctx)
	if err != nil {
		return Binding{}, err
	}
	if cfg.Token == "" || cfg.BotUsername == "" {
		return Binding{}, ErrNoToken
	}
	t.CancelBind()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.base == nil {
		return Binding{}, errors.New("监控任务还没启动")
	}
	code := randomCode()
	bd := &Binding{
		Link:      "https://t.me/" + cfg.BotUsername + "?start=" + code,
		ExpiresAt: store.Ms(t.Now().Add(BindTTL)),
		code:      code,
	}
	bctx, stop := context.WithTimeout(t.base, BindTTL)
	b, err := t.bot(cfg.Token,
		bot.WithAllowedUpdates(bot.AllowedUpdates{"message"}),
		bot.WithNotAsyncHandlers(),
		bot.WithDefaultHandler(func(ctx context.Context, b *bot.Bot, u *models.Update) { t.onUpdate(ctx, b, u, code, stop) }),
		bot.WithErrorsHandler(func(err error) {
			if bctx.Err() != nil {
				return
			}
			msg := clean(err, cfg.Token).Error()
			if errors.Is(err, bot.ErrorConflict) {
				msg = "这个机器人设置了 Webhook，或者别的程序也在收它的消息，绑定收不到 /start。请换一个机器人，或先停掉那边。"
			}
			t.mu.Lock()
			if t.binding != nil && t.binding.code == code {
				t.binding.Error = msg
			}
			t.mu.Unlock()
		}),
	)
	if err != nil {
		stop()
		return Binding{}, clean(err, cfg.Token)
	}
	done := make(chan struct{})
	t.binding, t.stop, t.done = bd, stop, done
	go func() {
		defer close(done)
		b.Start(bctx)
		t.mu.Lock()
		if t.binding != nil && t.binding.code == code {
			t.binding, t.stop, t.done = nil, nil, nil
		}
		t.mu.Unlock()
	}()
	return *bd, nil
}

// CancelBind 结束正在进行的绑定，等收消息的任务退出。
func (t *Telegram) CancelBind() {
	t.mu.Lock()
	stop, done := t.stop, t.done
	t.binding, t.stop, t.done = nil, nil, nil
	t.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// Binding 返回正在进行的绑定，没有时返回 nil。
func (t *Telegram) Binding() *Binding {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.binding == nil {
		return nil
	}
	b := *t.binding
	return &b
}

func chatName(c models.Chat) string {
	name := strings.TrimSpace(c.FirstName + " " + c.LastName)
	if c.Username != "" {
		if name != "" {
			name += " "
		}
		name += "@" + c.Username
	}
	if name == "" {
		name = fmt.Sprint(c.ID)
	}
	return name
}

func (t *Telegram) onUpdate(ctx context.Context, b *bot.Bot, u *models.Update, code string, stop context.CancelFunc) {
	m := u.Message
	if m == nil || m.Chat.Type != models.ChatTypePrivate {
		return
	}
	arg, ok := strings.CutPrefix(m.Text, "/start")
	if !ok {
		return
	}
	reply := func(text string) {
		if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: m.Chat.ID, Text: text}); err != nil {
			slog.Warn("Telegram 回复绑定消息失败", "err", err)
		}
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(arg)), []byte(code)) != 1 {
		reply("验证码不对或已经过期，请回 Tillbox 后台重新点“绑定”。")
		return
	}
	chat := &store.TelegramChat{ChatID: m.Chat.ID, Name: chatName(m.Chat), CreatedAt: store.Ms(t.Now())}
	_, err := t.DB.NewInsert().Model(chat).On("CONFLICT (chat_id) DO UPDATE").Set("name = EXCLUDED.name").Exec(ctx)
	if err != nil {
		slog.Error("保存 Telegram 绑定失败", "err", err)
		reply("绑定失败：Tillbox 保存时出错，请稍后在后台重试。")
		return
	}
	reply("绑定成功。Tillbox 的告警会发到这里。")
	stop()
}

// Chats 返回绑定的会话。
func (t *Telegram) Chats(ctx context.Context) ([]store.TelegramChat, error) {
	chats := make([]store.TelegramChat, 0)
	err := t.DB.NewSelect().Model(&chats).Order("id ASC").Scan(ctx)
	return chats, err
}

// Unbind 解绑一个会话。
func (t *Telegram) Unbind(ctx context.Context, id int64) error {
	_, err := t.DB.NewDelete().Model((*store.TelegramChat)(nil)).Where("id = ?", id).Exec(ctx)
	return err
}

// Send 发给全部绑定的会话，每个会话的结果记在库里。至少送达一个就算成功。
func (t *Telegram) Send(ctx context.Context, text string) error {
	cfg, err := t.Settings.Telegram(ctx)
	if err != nil {
		return err
	}
	if cfg.Token == "" {
		return ErrNoToken
	}
	chats, err := t.Chats(ctx)
	if err != nil {
		return err
	}
	if len(chats) == 0 {
		return ErrNoChats
	}
	if r := []rune(text); len(r) > maxText {
		text = string(r[:maxText]) + "\n……"
	}
	b, err := t.bot(cfg.Token)
	if err != nil {
		return clean(err, cfg.Token)
	}
	var first error
	delivered := false
	for i := range chats {
		c := &chats[i]
		sctx, cancel := context.WithTimeout(ctx, sendTimeout)
		_, err := b.SendMessage(sctx, &bot.SendMessageParams{
			ChatID: c.ChatID, Text: text, LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
		})
		cancel()
		now := store.Ms(t.Now())
		if err != nil {
			err = clean(err, cfg.Token)
			if first == nil {
				first = err
			}
			c.ErrorAt, c.Error = now, err.Error()
			_, _ = t.DB.NewUpdate().Model(c).Column("error_at", "error").WherePK().Exec(ctx)
			continue
		}
		delivered = true
		c.SentAt, c.Error = now, ""
		_, _ = t.DB.NewUpdate().Model(c).Column("sent_at", "error").WherePK().Exec(ctx)
	}
	if delivered {
		return nil
	}
	return first
}
