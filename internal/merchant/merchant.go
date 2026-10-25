// Package merchant 管理接入方（易支付里的商户 pid）。
package merchant

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/uptrace/bun"

	"tillbox/internal/epay"
	"tillbox/internal/secret"
	"tillbox/internal/store"
)

// FirstID 是第一个商户的编号。
const FirstID = 1001

var ErrNotFound = errors.New("商户不存在")

// Merchant 是解密后的商户。
type Merchant struct {
	store.Merchant
	Key       string         // 明文 MD5 密钥
	PublicKey *rsa.PublicKey // 解析好的 RSA 公钥，没有为 nil
}

type Service struct {
	db  *bun.DB
	box *secret.Box
}

func New(db *bun.DB, box *secret.Box) *Service {
	return &Service{db: db, box: box}
}

func (s *Service) decode(m *store.Merchant) (*Merchant, error) {
	key, err := s.box.Open(m.MD5Key)
	if err != nil {
		return nil, fmt.Errorf("商户 %d 的密钥解密失败：%w", m.ID, err)
	}
	out := &Merchant{Merchant: *m, Key: key}
	if m.RSAPublicKey != "" {
		pk, err := epay.ParsePublicKey(m.RSAPublicKey)
		if err != nil {
			return nil, fmt.Errorf("商户 %d 的 RSA 公钥无效：%w", m.ID, err)
		}
		out.PublicKey = pk
	}
	return out, nil
}

// Get 取一个商户（含解密后的密钥）。
func (s *Service) Get(ctx context.Context, id int64) (*Merchant, error) {
	var m store.Merchant
	err := s.db.NewSelect().Model(&m).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.decode(&m)
}

func (s *Service) List(ctx context.Context) ([]store.Merchant, error) {
	var ms []store.Merchant
	err := s.db.NewSelect().Model(&ms).Order("id ASC").Scan(ctx)
	return ms, err
}

// Input 是新建或修改商户时可改的字段。
type Input struct {
	Name         string `json:"name"`
	RSAPublicKey string `json:"rsa_public_key"`
	KeyType      int    `json:"key_type"`
	Domains      string `json:"domains"`
	Enabled      bool   `json:"enabled"`
	ChannelID    int64  `json:"channel_id"` // 0：新建时用第一个启用的渠道，修改时保留原渠道
	Remark       string `json:"remark"`
}

// channelFor 检查商户要用的渠道存在；没指定时新建商户用第一个启用的渠道（没有就用第一个渠道）。
func (s *Service) channelFor(ctx context.Context, id int64) (int64, error) {
	if id != 0 {
		ok, err := s.db.NewSelect().Model((*store.Channel)(nil)).Where("id = ?", id).Exists(ctx)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, errors.New("所选的支付渠道不存在")
		}
		return id, nil
	}
	var ids []int64
	err := s.db.NewSelect().Model((*store.Channel)(nil)).Column("id").OrderExpr("enabled DESC, id ASC").Limit(1).Scan(ctx, &ids)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return ids[0], nil
}

func (in *Input) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 64 {
		return errors.New("商户名称不能为空，且不超过 64 个字")
	}
	in.RSAPublicKey = strings.TrimSpace(in.RSAPublicKey)
	if in.RSAPublicKey != "" {
		if _, err := epay.ParsePublicKey(in.RSAPublicKey); err != nil {
			return fmt.Errorf("RSA 公钥无效：%w", err)
		}
	}
	switch in.KeyType {
	case store.KeyTypeAny:
	case store.KeyTypeRSAOnly:
		if in.RSAPublicKey == "" {
			return errors.New("只收 RSA 签名时必须填写商户 RSA 公钥")
		}
	default:
		return errors.New("密钥类型不正确")
	}
	domains, err := NormalizeDomains(in.Domains)
	if err != nil {
		return err
	}
	in.Domains = domains
	if len([]rune(in.Remark)) > 500 {
		return errors.New("备注不超过 500 个字")
	}
	return nil
}

// NormalizeDomains 整理域名白名单：一行一个，支持 *.example.com，去重、转小写。
func NormalizeDomains(s string) (string, error) {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' || r == ' ' || r == '\r' || r == '\t' }) {
		d := strings.ToLower(strings.TrimSpace(line))
		if d == "" {
			continue
		}
		host := strings.TrimPrefix(d, "*.")
		if strings.ContainsAny(host, "/:*?#@") || !strings.Contains(host, ".") && host != "localhost" {
			return "", fmt.Errorf("域名 %q 格式不正确，只填域名，例如 example.com 或 *.example.com", d)
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return strings.Join(out, "\n"), nil
}

// AllowsURL 判断地址的域名是否在白名单里。白名单为空表示不限制。
func (m *Merchant) AllowsURL(raw string) bool {
	if strings.TrimSpace(m.Domains) == "" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, d := range strings.Split(m.Domains, "\n") {
		if d == host {
			return true
		}
		if suffix, ok := strings.CutPrefix(d, "*."); ok && strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// NewKey 生成 32 位字母数字密钥。
func NewKey() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// Create 新建商户，返回商户和明文密钥。
func (s *Service) Create(ctx context.Context, in Input) (*store.Merchant, string, error) {
	if err := in.normalize(); err != nil {
		return nil, "", err
	}
	chID, err := s.channelFor(ctx, in.ChannelID)
	if err != nil {
		return nil, "", err
	}
	key := NewKey()
	sealed, err := s.box.Seal(key)
	if err != nil {
		return nil, "", err
	}
	now := store.Now()
	m := &store.Merchant{
		Name: in.Name, MD5Key: sealed, RSAPublicKey: in.RSAPublicKey, KeyType: in.KeyType,
		Domains: in.Domains, Enabled: in.Enabled, ChannelID: chID, Remark: in.Remark, CreatedAt: now, UpdatedAt: now,
	}
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var maxID sql.NullInt64
		if err := tx.NewSelect().Model((*store.Merchant)(nil)).ColumnExpr("MAX(id)").Scan(ctx, &maxID); err != nil {
			return err
		}
		m.ID = max(maxID.Int64+1, FirstID)
		_, err := tx.NewInsert().Model(m).Exec(ctx)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return m, key, nil
}

// Update 修改商户资料（不改密钥）。
// expected 是页面载入时的修改时间，和库里的对不上时返回 store.ErrConflict；为负数时不检查。
func (s *Service) Update(ctx context.Context, id int64, in Input, expected int64) (*store.Merchant, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	var m store.Merchant
	if err := s.db.NewSelect().Model(&m).Where("id = ?", id).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if in.ChannelID != 0 && in.ChannelID != m.ChannelID {
		if _, err := s.channelFor(ctx, in.ChannelID); err != nil {
			return nil, err
		}
		m.ChannelID = in.ChannelID
	}
	m.Name, m.RSAPublicKey, m.KeyType = in.Name, in.RSAPublicKey, in.KeyType
	m.Domains, m.Enabled, m.Remark = in.Domains, in.Enabled, in.Remark
	old := m.UpdatedAt
	if expected >= 0 && old != expected {
		return nil, store.ErrConflict
	}
	m.UpdatedAt = store.Revise(old)
	// 只在修改时间还是刚才读到的值时更新，读和写之间被别人改了也算冲突。
	res, err := s.db.NewUpdate().Model(&m).
		Column("name", "rsa_public_key", "key_type", "domains", "enabled", "channel_id", "remark", "updated_at").
		WherePK().Where("updated_at = ?", old).Exec(ctx)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, store.ErrConflict
	}
	return &m, nil
}

// ResetKey 重新生成 MD5 密钥，旧密钥立即失效。
func (s *Service) ResetKey(ctx context.Context, id int64) (string, error) {
	key := NewKey()
	sealed, err := s.box.Seal(key)
	if err != nil {
		return "", err
	}
	res, err := s.db.NewUpdate().Model((*store.Merchant)(nil)).
		Set("md5_key = ?", sealed).Set("updated_at = ?", store.Now()).
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return key, nil
}

// Delete 删除没有订单的商户。有订单的只能停用。
func (s *Service) Delete(ctx context.Context, id int64) error {
	n, err := s.db.NewSelect().Model((*store.Order)(nil)).Where("merchant_id = ?", id).Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return errors.New("这个商户已经有订单，只能停用，不能删除")
	}
	res, err := s.db.NewDelete().Model((*store.Merchant)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
