package settings

import (
	"context"
	"errors"
)

type Retention struct {
	Enabled     bool `json:"enabled"`
	RawDays     int  `json:"raw_days"`
	AttemptDays int  `json:"attempt_days"`
	AuditDays   int  `json:"audit_days"`
}

func (s *Service) Retention(ctx context.Context) (Retention, error) {
	v := Retention{Enabled: true, RawDays: 90, AttemptDays: 90, AuditDays: 365}
	_, err := s.load(ctx, KeyRetention, &v)
	return v, err
}

func (s *Service) SaveRetention(ctx context.Context, v Retention) error {
	if v.RawDays < 30 || v.RawDays > 3650 || v.AttemptDays < 30 || v.AttemptDays > 3650 || v.AuditDays < 90 || v.AuditDays > 3650 {
		return errors.New("流水和通知保留 30–3650 天，审计保留 90–3650 天")
	}
	return s.save(ctx, KeyRetention, v)
}

// Backup 是 SQLite 自动备份的设置。
type Backup struct {
	Enabled bool `json:"enabled"`
	Keep    int  `json:"keep"` // 留几份，1–90
}

func (s *Service) Backup(ctx context.Context) (Backup, error) {
	v := Backup{Keep: 7}
	_, err := s.load(ctx, KeyBackup, &v)
	return v, err
}

func (s *Service) SaveBackup(ctx context.Context, v Backup) error {
	if v.Keep < 1 || v.Keep > 90 {
		return errors.New("备份要留 1–90 份")
	}
	return s.save(ctx, KeyBackup, v)
}

// Update 是新版本提醒的设置。
type Update struct {
	Check bool `json:"check"` // 定时向 GitHub 查有没有新版本
}

func (s *Service) Update(ctx context.Context) (Update, error) {
	v := Update{Check: true}
	_, err := s.load(ctx, KeyUpdate, &v)
	return v, err
}

func (s *Service) SaveUpdate(ctx context.Context, v Update) error {
	return s.save(ctx, KeyUpdate, v)
}
