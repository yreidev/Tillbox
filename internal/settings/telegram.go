package settings

import (
	"context"
	"errors"
)

// Telegram 是给管理员发通知的 Telegram 机器人。Token 只在服务端用，存库时用主密钥加密。
type Telegram struct {
	Token       string `json:"token,omitempty"`
	BotID       int64  `json:"bot_id"`
	BotUsername string `json:"bot_username"`
	// RemindHours 是告警持续未恢复时再提醒的间隔（小时），0 表示不再提醒。
	RemindHours int `json:"remind_hours"`
	// Paid、Claims 是收款到账、付款人申诉要不要发。告警总是发，不能关：
	// 后台配好 Telegram 后宿主机巡检不再重复发业务告警。
	Paid   bool `json:"paid"`
	Claims bool `json:"claims"`
}

const keyTelegram = "telegram"

// DefaultTelegram 是没设置过时的值；旧设置里没有的项也取这里的值。
func DefaultTelegram() Telegram {
	return Telegram{RemindHours: 1, Paid: true, Claims: true}
}

func (s *Service) Telegram(ctx context.Context) (Telegram, error) {
	v := DefaultTelegram()
	_, err := s.load(ctx, keyTelegram, &v)
	if err == nil && v.Token != "" {
		v.Token, err = s.box.Open(v.Token)
	}
	return v, err
}

func (s *Service) SaveTelegram(ctx context.Context, v Telegram) error {
	if v.RemindHours < 0 || v.RemindHours > 24 {
		return errors.New("再提醒间隔须为 0–24 小时")
	}
	if len(v.Token) > 200 {
		return errors.New("机器人 Token 过长")
	}
	if v.Token != "" {
		sealed, err := s.box.Seal(v.Token)
		if err != nil {
			return err
		}
		v.Token = sealed
	}
	return s.save(ctx, keyTelegram, v)
}
