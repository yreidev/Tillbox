// Package admin 是后台：管理员登录和后台接口。
package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/uptrace/bun"
	"golang.org/x/crypto/bcrypt"

	"tillbox/internal/store"
)

const (
	CookieName = "tillbox_session"
	SessionTTL = 7 * 24 * time.Hour
	// 同一 IP 15 分钟内登录失败超过 10 次就先拒绝。
	loginWindow   = 15 * time.Minute
	loginMaxFails = 10
	minPassword   = 8
	// Actor 是审计日志里管理员操作的署名（只有一个管理员）。
	Actor = "管理员"
)

var (
	ErrBadLogin     = errors.New("密码不对")
	ErrTooManyTries = errors.New("登录失败次数太多，请 15 分钟后再试")
	ErrNoSession    = errors.New("请先登录")
)

type Auth struct {
	db *bun.DB

	mu    sync.Mutex
	fails map[string][]time.Time
}

func NewAuth(db *bun.DB) *Auth {
	return &Auth{db: db, fails: map[string][]time.Time{}}
}

// PasswordCost 是管理员密码的 bcrypt 强度，可以在测试里调低：开 race 检测时，强度 12 算一次要好几秒。
var PasswordCost = 12

var dummyHash = sync.OnceValue(func() string {
	h, _ := bcrypt.GenerateFromPassword([]byte(randomString(16)), PasswordCost)
	return string(h)
})

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (a *Auth) recentFails(ip string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range a.fails[ip] {
		if now.Sub(t) < loginWindow {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(a.fails, ip)
	} else {
		a.fails[ip] = keep
	}
	return keep
}

// Login 校验管理员密码，成功后建会话，返回会话令牌（明文只在 cookie 里）。
func (a *Auth) Login(ctx context.Context, password, ip, userAgent string) (string, *store.Admin, error) {
	now := time.Now()
	a.mu.Lock()
	if len(a.recentFails(ip, now)) >= loginMaxFails {
		a.mu.Unlock()
		return "", nil, ErrTooManyTries
	}
	a.mu.Unlock()

	var ad store.Admin
	err := a.db.NewSelect().Model(&ad).Order("id").Limit(1).Scan(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	hash := ad.PasswordHash
	if hash == "" {
		// 还没有管理员时也算一次哈希，响应时间和密码错误时一样。
		hash = dummyHash()
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || ad.ID == 0 {
		a.mu.Lock()
		a.fails[ip] = append(a.recentFails(ip, now), now)
		a.mu.Unlock()
		return "", nil, ErrBadLogin
	}
	a.mu.Lock()
	delete(a.fails, ip)
	a.mu.Unlock()

	token := randomString(32)
	s := &store.Session{
		TokenHash: hashToken(token), AdminID: ad.ID, IP: ip, UserAgent: truncate(userAgent, 255),
		CreatedAt: store.Ms(now), ExpiresAt: store.Ms(now.Add(SessionTTL)),
	}
	if _, err := a.db.NewInsert().Model(s).Exec(ctx); err != nil {
		return "", nil, err
	}
	_ = store.Audit(ctx, a.db, Actor, "auth.login", "", "IP "+ip)
	return token, &ad, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// Session 按令牌找当前管理员。
func (a *Auth) Session(ctx context.Context, token string) (*store.Admin, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	var s store.Session
	err := a.db.NewSelect().Model(&s).Where("token_hash = ?", hashToken(token)).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	if s.ExpiresAt <= store.Now() {
		return nil, ErrNoSession
	}
	var ad store.Admin
	if err := a.db.NewSelect().Model(&ad).Where("id = ?", s.AdminID).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoSession
		}
		return nil, err
	}
	return &ad, nil
}

func (a *Auth) Logout(ctx context.Context, token string) error {
	_, err := a.db.NewDelete().Model((*store.Session)(nil)).Where("token_hash = ?", hashToken(token)).Exec(ctx)
	return err
}

// ChangePassword 修改密码，并让这个管理员的其他会话全部失效。
func (a *Auth) ChangePassword(ctx context.Context, ad *store.Admin, oldPassword, newPassword, keepToken string) error {
	if bcrypt.CompareHashAndPassword([]byte(ad.PasswordHash), []byte(oldPassword)) != nil {
		return errors.New("原密码不对")
	}
	if err := checkPassword(newPassword); err != nil {
		return err
	}
	if err := setPassword(ctx, a.db, ad.ID, newPassword); err != nil {
		return err
	}
	_, err := a.db.NewDelete().Model((*store.Session)(nil)).
		Where("admin_id = ?", ad.ID).Where("token_hash <> ?", hashToken(keepToken)).Exec(ctx)
	if err != nil {
		return err
	}
	return store.Audit(ctx, a.db, Actor, "auth.password", "", "修改了密码")
}

// CleanupSessions 删掉过期会话。
func (a *Auth) CleanupSessions(ctx context.Context) error {
	_, err := a.db.NewDelete().Model((*store.Session)(nil)).Where("expires_at <= ?", store.Now()).Exec(ctx)
	return err
}

func checkPassword(p string) error {
	if len([]rune(p)) < minPassword {
		return errors.New("密码至少 8 位")
	}
	if len(p) > 72 {
		return errors.New("密码不能超过 72 个字节")
	}
	return nil
}

func setPassword(ctx context.Context, db bun.IDB, adminID int64, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), PasswordCost)
	if err != nil {
		return err
	}
	_, err = db.NewUpdate().Model((*store.Admin)(nil)).
		Set("password_hash = ?", string(hash)).Set("updated_at = ?", store.Now()).
		Where("id = ?", adminID).Exec(ctx)
	return err
}

// EnsureAdmin 在还没有管理员时建一个，返回随机生成的密码。已有管理员时返回空串。
func EnsureAdmin(ctx context.Context, db *bun.DB) (string, error) {
	n, err := db.NewSelect().Model((*store.Admin)(nil)).Count(ctx)
	if err != nil || n > 0 {
		return "", err
	}
	password := randomString(12)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), PasswordCost)
	if err != nil {
		return "", err
	}
	now := store.Now()
	_, err = db.NewInsert().Model(&store.Admin{PasswordHash: string(hash), CreatedAt: now, UpdatedAt: now}).Exec(ctx)
	return password, err
}

// ResetPassword 给管理员设一个新的随机密码并清掉所有登录会话；还没有管理员时新建。
func ResetPassword(ctx context.Context, db *bun.DB) (string, error) {
	password := randomString(12)
	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var ad store.Admin
		err := tx.NewSelect().Model(&ad).Order("id").Limit(1).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			now := store.Now()
			ad = store.Admin{PasswordHash: "!", CreatedAt: now, UpdatedAt: now}
			if _, err := tx.NewInsert().Model(&ad).Exec(ctx); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := setPassword(ctx, tx, ad.ID, password); err != nil {
			return err
		}
		if _, err := tx.NewDelete().Model((*store.Session)(nil)).Where("admin_id = ?", ad.ID).Exec(ctx); err != nil {
			return err
		}
		return store.Audit(ctx, tx, "命令行", "auth.reset", "", "重置了管理员密码")
	})
	return password, err
}
