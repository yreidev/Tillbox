// Package settings 管理存在库里的业务设置。改了立即生效，不需要重启。
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"tillbox/internal/epay"
	"tillbox/internal/secret"
	"tillbox/internal/store"
)

// Payment 是收款和查账的节奏设置。
type Payment struct {
	OrderTTLSeconds   int `json:"order_ttl_seconds"`   // 订单有效期，60–1800
	CooldownSeconds   int `json:"cooldown_seconds"`    // 应付金额释放后的冷却时间，60–3600
	MaxTailCents      int `json:"max_tail_cents"`      // 尾数上限，1–99 分
	NormalInterval    int `json:"normal_interval"`     // 没有待付订单时的查账间隔（秒）
	ActiveInterval    int `json:"active_interval"`     // 有待付订单时的查账间隔（秒）
	MinInterval       int `json:"min_interval"`        // 两轮查账的最小间隔（秒）
	SafetyDelaySecond int `json:"safety_delay_second"` // 查询结束时间 = 现在 − 安全延迟
}

func DefaultPayment() Payment {
	return Payment{
		OrderTTLSeconds:   300,
		CooldownSeconds:   600,
		MaxTailCents:      99,
		NormalInterval:    60,
		ActiveInterval:    8,
		MinInterval:       8,
		SafetyDelaySecond: 10,
	}
}

func (p Payment) Validate() error {
	switch {
	case p.OrderTTLSeconds < 60 || p.OrderTTLSeconds > 1800:
		return errors.New("订单有效期要在 60–1800 秒之间")
	case p.CooldownSeconds < 60 || p.CooldownSeconds > 3600:
		return errors.New("冷却时间要在 60–3600 秒之间")
	case p.MaxTailCents < 1 || p.MaxTailCents > 99:
		return errors.New("尾数上限要在 1–99 分之间")
	case p.NormalInterval < 10 || p.NormalInterval > 3600:
		return errors.New("常规查账间隔要在 10–3600 秒之间")
	case p.MinInterval < 3 || p.MinInterval > 300:
		return errors.New("最小查账间隔要在 3–300 秒之间")
	case p.ActiveInterval < p.MinInterval || p.ActiveInterval > p.NormalInterval:
		return errors.New("活跃查账间隔要不小于最小间隔、不大于常规间隔")
	case p.SafetyDelaySecond < 0 || p.SafetyDelaySecond > 120:
		return errors.New("安全延迟要在 0–120 秒之间")
	}
	return nil
}

func (p Payment) OrderTTL() time.Duration { return time.Duration(p.OrderTTLSeconds) * time.Second }
func (p Payment) Cooldown() time.Duration { return time.Duration(p.CooldownSeconds) * time.Second }

// Platform 是平台自己的 RSA 密钥对，给 V2 响应和通知签名。
type Platform struct {
	PrivateKey string `json:"private_key"` // 主密钥加密
	PublicKey  string `json:"public_key"`
}

// Site 是展示用的站点信息。
type Site struct {
	Name string `json:"name"`
}

const (
	keyPayment  = "payment"
	keyPlatform = "platform"
	keySite     = "site"
)

type Service struct {
	db  *bun.DB
	box *secret.Box

	mu    sync.RWMutex
	cache map[string]string
}

func New(db *bun.DB, box *secret.Box) *Service {
	return &Service{db: db, box: box, cache: map[string]string{}}
}

func (s *Service) Box() *secret.Box { return s.box }

func (s *Service) load(ctx context.Context, key string, out any) (bool, error) {
	s.mu.RLock()
	raw, ok := s.cache[key]
	s.mu.RUnlock()
	if !ok {
		var row store.Setting
		err := s.db.NewSelect().Model(&row).Where("key = ?", key).Scan(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		// 没存过的键也缓存（空串），免得每次都查库。
		raw = row.Value
		s.mu.Lock()
		s.cache[key] = raw
		s.mu.Unlock()
	}
	if raw == "" {
		return false, nil
	}
	return true, json.Unmarshal([]byte(raw), out)
}

func (s *Service) save(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	row := store.Setting{Key: key, Value: string(raw), UpdatedAt: store.Now()}
	_, err = s.db.NewInsert().Model(&row).
		On("CONFLICT (key) DO UPDATE").
		Set("value = EXCLUDED.value").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cache[key] = string(raw)
	s.mu.Unlock()
	return nil
}

func (s *Service) Payment(ctx context.Context) (Payment, error) {
	p := DefaultPayment()
	if _, err := s.load(ctx, keyPayment, &p); err != nil {
		return p, err
	}
	return p, nil
}

func (s *Service) SavePayment(ctx context.Context, p Payment) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return s.save(ctx, keyPayment, p)
}

func (s *Service) Site(ctx context.Context) (Site, error) {
	site := Site{Name: "Tillbox"}
	if _, err := s.load(ctx, keySite, &site); err != nil {
		return site, err
	}
	return site, nil
}

func (s *Service) SaveSite(ctx context.Context, site Site) error {
	return s.save(ctx, keySite, site)
}

// Platform 返回平台密钥对；还没有就生成一对存起来。
func (s *Service) Platform(ctx context.Context) (Platform, error) {
	var p Platform
	ok, err := s.load(ctx, keyPlatform, &p)
	if err != nil {
		return p, err
	}
	if ok && p.PrivateKey != "" {
		return p, nil
	}
	return s.RegeneratePlatform(ctx)
}

// PlatformKey 返回解密后的平台私钥。
func (s *Service) PlatformKey(ctx context.Context) (Platform, string, error) {
	p, err := s.Platform(ctx)
	if err != nil {
		return p, "", err
	}
	priv, err := s.box.Open(p.PrivateKey)
	if err != nil {
		return p, "", fmt.Errorf("平台私钥解密失败：%w", err)
	}
	return p, priv, nil
}

// RegeneratePlatform 生成新的平台密钥对。旧公钥立即失效，商户要换成新公钥。
func (s *Service) RegeneratePlatform(ctx context.Context) (Platform, error) {
	priv, pub, err := epay.GenerateKeyPair()
	if err != nil {
		return Platform{}, err
	}
	sealed, err := s.box.Seal(priv)
	if err != nil {
		return Platform{}, err
	}
	p := Platform{PrivateKey: sealed, PublicKey: pub}
	if err := s.save(ctx, keyPlatform, p); err != nil {
		return Platform{}, err
	}
	return p, nil
}

// Raw 读写不需要类型的内部状态（例如查账进度）。
func (s *Service) Raw(ctx context.Context, key string, out any) (bool, error) {
	return s.load(ctx, key, out)
}

func (s *Service) SaveRaw(ctx context.Context, key string, v any) error {
	return s.save(ctx, key, v)
}
