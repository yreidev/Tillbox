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
	_, err := s.load(ctx, "retention", &v)
	return v, err
}

func (s *Service) SaveRetention(ctx context.Context, v Retention) error {
	if v.RawDays < 30 || v.RawDays > 3650 || v.AttemptDays < 30 || v.AttemptDays > 3650 || v.AuditDays < 90 || v.AuditDays > 3650 {
		return errors.New("流水和通知保留 30–3650 天，审计保留 90–3650 天")
	}
	return s.save(ctx, "retention", v)
}
