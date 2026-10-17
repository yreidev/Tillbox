package settings

import (
	"context"
	"errors"
)

// Telegram 是告警用的 Telegram 机器人。Token 只在服务端用，存库时用主密钥加密。
type Telegram struct {
	Token       string `json:"token,omitempty"`
	BotID       int64  `json:"bot_id"`
	BotUsername string `json:"bot_username"`
	// RemindHours 是告警持续未恢复时再提醒的间隔（小时），0 表示不再提醒。
	RemindHours int `json:"remind_hours"`
}

const keyTelegram = "telegram"

func (s *Service) Telegram(ctx context.Context) (Telegram, error) {
	v := Telegram{RemindHours: 1}
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
