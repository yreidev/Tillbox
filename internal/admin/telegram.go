package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"tillbox/internal/monitor"
	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// telegramView 是“设置 → 管理员通知”页的内容；监控页也带上它，显示告警发没发出去。
type telegramView struct {
	Configured  bool                 `json:"configured"`
	BotUsername string               `json:"bot_username"`
	RemindHours int                  `json:"remind_hours"`
	Paid        bool                 `json:"paid"`
	Claims      bool                 `json:"claims"`
	Chats       []store.TelegramChat `json:"chats"`
	Binding     *monitor.Binding     `json:"binding"`
	State       monitor.State        `json:"state"`
	Revision    int64                `json:"revision"` // 保存时放进 If-Match，检查有没有在别处改过
}

func (a *API) telegramView(ctx context.Context) (telegramView, error) {
	cfg, err := a.Settings.Telegram(ctx)
	if err != nil {
		return telegramView{}, err
	}
	chats, err := a.Monitor.Telegram.Chats(ctx)
	if err != nil {
		return telegramView{}, err
	}
	state, err := a.Monitor.State(ctx)
	if err != nil {
		return telegramView{}, err
	}
	rev, err := a.Settings.Revision(ctx, settings.KeyTelegram)
	if err != nil {
		return telegramView{}, err
	}
	return telegramView{
		Configured: cfg.Token != "", BotUsername: cfg.BotUsername, RemindHours: cfg.RemindHours, Paid: cfg.Paid, Claims: cfg.Claims,
		Chats: chats, Binding: a.Monitor.Telegram.Binding(), State: state, Revision: rev,
	}, nil
}

func (a *API) getTelegram(w http.ResponseWriter, r *http.Request) error {
	tg, err := a.telegramView(r.Context())
	if err != nil {
		return err
	}
	ok(w, tg)
	return nil
}

func (a *API) saveTelegram(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token string `json:"token"` // 留空表示不改
		monitor.Prefs
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.RemindHours < 0 || in.RemindHours > 24 {
		return badRequest("再提醒间隔须为 0–24 小时")
	}
	var cfg settings.Telegram
	_, err := a.guard(r, []string{settings.KeyTelegram}, func() error {
		var err error
		if cfg, err = a.Monitor.Telegram.Configure(r.Context(), in.Token, in.Prefs); err != nil {
			return badRequest(err.Error())
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "telegram.update", "@"+cfg.BotUsername,
		"到账："+onOff(cfg.Paid)+"，申诉："+onOff(cfg.Claims)+"，告警再提醒间隔 "+strconv.Itoa(cfg.RemindHours)+" 小时")
	tg, err := a.telegramView(r.Context())
	if err != nil {
		return err
	}
	ok(w, tg)
	return nil
}

func onOff(v bool) string {
	if v {
		return "发"
	}
	return "不发"
}

func (a *API) clearTelegram(w http.ResponseWriter, r *http.Request) error {
	if err := a.Monitor.Telegram.Clear(r.Context()); err != nil {
		return err
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "telegram.clear", "", "")
	ok(w, map[string]any{})
	return nil
}

func (a *API) bindTelegram(w http.ResponseWriter, r *http.Request) error {
	b, err := a.Monitor.Telegram.Bind(r.Context())
	if errors.Is(err, monitor.ErrNoToken) {
		return badRequest(err.Error())
	}
	if err != nil {
		return err
	}
	ok(w, b)
	return nil
}

func (a *API) cancelBindTelegram(w http.ResponseWriter, _ *http.Request) error {
	a.Monitor.Telegram.CancelBind()
	ok(w, map[string]any{})
	return nil
}

// testTelegram 用示例数据发测试消息：kind 是告警开始、告警恢复、收款到账、付款申诉之一，或 all（每种各一条）。
func (a *API) testTelegram(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Kind string `json:"kind"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Kind != "all" && !slices.Contains(monitor.TestKinds, in.Kind) {
		return badRequest(monitor.ErrTestKind.Error())
	}
	if err := a.Monitor.Test(r.Context(), in.Kind); err != nil {
		return badRequest("发送失败：" + err.Error())
	}
	ok(w, map[string]any{})
	return nil
}

func (a *API) unbindTelegram(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := a.Monitor.Telegram.Unbind(r.Context(), id); err != nil {
		return err
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "telegram.unbind", strconv.FormatInt(id, 10), "")
	ok(w, map[string]any{})
	return nil
}
