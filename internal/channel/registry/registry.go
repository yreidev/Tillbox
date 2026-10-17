// Package registry 是渠道注册表：从渠道表加载渠道实例，后台增删改后立即生效。
// 下单、收银台、查账、回调都通过它找到订单所属的渠道实例。
package registry

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/uptrace/bun"

	"tillbox/internal/channel"
	"tillbox/internal/channel/alipayqr"
	"tillbox/internal/channel/fake"
	"tillbox/internal/channel/upstream"
	"tillbox/internal/secret"
	"tillbox/internal/store"
)

var ErrNotFound = errors.New("渠道不存在")

// Registry 持有全部渠道实例。
type Registry struct {
	db  *bun.DB
	box *secret.Box
	// Demo 为真时允许演示渠道（TILLBOX_CHANNEL=fake）。
	Demo bool
	// OnChange 在渠道增删改、重新加载后调用（同步查账任务）。
	OnChange func()

	mu    sync.RWMutex
	rows  []store.Channel // 按编号升序
	insts map[int64]channel.Instance
	// saveMu 让新建和修改排队，查重和写库之间不会插进另一次保存。
	saveMu sync.Mutex
}

func New(db *bun.DB, box *secret.Box, demo bool) *Registry {
	return &Registry{db: db, box: box, Demo: demo, insts: map[int64]channel.Instance{}}
}

func (r *Registry) build(typ string) (channel.Instance, error) {
	switch typ {
	case store.ChannelAlipayQR:
		return alipayqr.New(r.box), nil
	case store.ChannelEpay:
		return upstream.New(r.box), nil
	case store.ChannelDemo:
		d := fake.New()
		d.Allowed = r.Demo
		return d, nil
	}
	return nil, fmt.Errorf("不认识的渠道类型 %q", typ)
}

// Load 从库里重新加载全部渠道：已有实例原地更新，新渠道建实例，删掉的渠道移除。
func (r *Registry) Load(ctx context.Context) error {
	var rows []store.Channel
	if err := r.db.NewSelect().Model(&rows).Order("id ASC").Scan(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	insts := make(map[int64]channel.Instance, len(rows))
	kept := rows[:0]
	for _, row := range rows {
		inst, ok := r.insts[row.ID]
		if !ok {
			var err error
			if inst, err = r.build(row.Type); err != nil {
				r.mu.Unlock()
				return fmt.Errorf("渠道 %d：%w", row.ID, err)
			}
		}
		inst.Configure(row)
		insts[row.ID] = inst
		kept = append(kept, row)
	}
	r.rows, r.insts = kept, insts
	r.mu.Unlock()
	if r.OnChange != nil {
		r.OnChange()
	}
	return nil
}

// List 返回全部渠道（按编号升序）。
func (r *Registry) List() []store.Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.rows)
}

// Get 按编号取渠道实例和它的渠道表行。
func (r *Registry) Get(id int64) (channel.Instance, store.Channel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	inst, ok := r.insts[id]
	if !ok {
		return nil, store.Channel{}, false
	}
	i, _ := slices.BinarySearchFunc(r.rows, id, func(c store.Channel, id int64) int { return cmp.Compare(c.ID, id) })
	return inst, r.rows[i], true
}

// ByKey 按渠道实例键（订单和流水的 channel 列）取渠道实例。
func (r *Registry) ByKey(key string) (channel.Instance, store.Channel, bool) {
	return r.Get(store.ChannelID(key))
}

// Name 返回渠道名称，找不到时返回实例键。
func (r *Registry) Name(key string) string {
	if _, row, ok := r.ByKey(key); ok {
		return row.Name
	}
	return key
}

// Sources 返回全部推断型渠道（需要查账的），按编号升序。
func (r *Registry) Sources() []channel.LedgerSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []channel.LedgerSource
	for _, row := range r.rows {
		if s, ok := r.insts[row.ID].(channel.LedgerSource); ok {
			out = append(out, s)
		}
	}
	return out
}

// Demos 返回全部演示渠道，按编号升序。
func (r *Registry) Demos() []*fake.Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*fake.Channel
	for _, row := range r.rows {
		if d, ok := r.insts[row.ID].(*fake.Channel); ok {
			out = append(out, d)
		}
	}
	return out
}

// Input 是后台新建或修改渠道时提交的内容。Config 按渠道类型解析。
type Input struct {
	Type    string          `json:"type"`
	Name    string          `json:"name"`
	Enabled bool            `json:"enabled"`
	Remark  string          `json:"remark"`
	Config  json.RawMessage `json:"config"`
}

func (r *Registry) normalize(in *Input) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 32 {
		return channel.Invalid("渠道名称不能为空，且不超过 32 个字")
	}
	if len([]rune(in.Remark)) > 500 {
		return channel.Invalid("备注不超过 500 个字")
	}
	return nil
}

// merge 按类型校验配置并合并到当前配置。
func (r *Registry) merge(typ, cur string, in Input) (string, error) {
	switch typ {
	case store.ChannelAlipayQR:
		return alipayqr.Merge(r.box, cur, in.Config, in.Enabled)
	case store.ChannelEpay:
		return upstream.Merge(r.box, cur, in.Config, in.Enabled)
	case store.ChannelDemo:
		if !r.Demo {
			return "", channel.Invalid("演示渠道只能在本地演示模式（TILLBOX_CHANNEL=fake）下使用")
		}
		return "{}", nil
	}
	return "", channel.Invalid("不支持的渠道类型")
}

// distinct 检查经营码渠道不和别的经营码渠道共用应用 ID 或经营码。同一个账户配成两个渠道时，
// 两边查账都会看到同一笔付款，而金额尾数按渠道分配，一笔付款可能让两个渠道各有一单到账。
func (r *Registry) distinct(id int64, typ, cfg string) error {
	if typ != store.ChannelAlipayQR {
		return nil
	}
	c, err := alipayqr.ParseConfig(cfg)
	if err != nil {
		return err
	}
	for _, row := range r.List() {
		if row.ID == id || row.Type != store.ChannelAlipayQR {
			continue
		}
		o, err := alipayqr.ParseConfig(row.Config)
		if err != nil {
			continue
		}
		switch {
		case c.AppID != "" && c.AppID == o.AppID:
			return channel.Invalid("这个应用 ID 已经用在渠道「%s」上，同一个支付宝账户只能配一个经营码渠道", row.Name)
		case c.QRCodeURL != "" && c.QRCodeURL == o.QRCodeURL:
			return channel.Invalid("这张经营码已经用在渠道「%s」上，同一个支付宝账户只能配一个经营码渠道", row.Name)
		}
	}
	return nil
}

// Create 新建渠道。
func (r *Registry) Create(ctx context.Context, in Input) (*store.Channel, error) {
	if err := r.normalize(&in); err != nil {
		return nil, err
	}
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	cfg, err := r.merge(in.Type, "", in)
	if err != nil {
		return nil, err
	}
	if err := r.distinct(0, in.Type, cfg); err != nil {
		return nil, err
	}
	now := store.Now()
	row := &store.Channel{Type: in.Type, Name: in.Name, Enabled: in.Enabled, Config: cfg, Remark: in.Remark, CreatedAt: now, UpdatedAt: now}
	if _, err := r.db.NewInsert().Model(row).Exec(ctx); err != nil {
		return nil, err
	}
	return row, r.Load(ctx)
}

// Update 修改渠道（类型不能改）。
func (r *Registry) Update(ctx context.Context, id int64, in Input) (*store.Channel, error) {
	if err := r.normalize(&in); err != nil {
		return nil, err
	}
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	row, err := r.row(ctx, id)
	if err != nil {
		return nil, err
	}
	cfg, err := r.merge(row.Type, row.Config, in)
	if err != nil {
		return nil, err
	}
	if err := r.distinct(id, row.Type, cfg); err != nil {
		return nil, err
	}
	row.Name, row.Enabled, row.Remark, row.Config, row.UpdatedAt = in.Name, in.Enabled, in.Remark, cfg, store.Now()
	if _, err := r.db.NewUpdate().Model(row).Column("name", "enabled", "remark", "config", "updated_at").WherePK().Exec(ctx); err != nil {
		return nil, err
	}
	return row, r.Load(ctx)
}

// SaveConfig 直接换掉渠道的配置原文（例如生成新的应用密钥对）。
func (r *Registry) SaveConfig(ctx context.Context, id int64, cfg string) error {
	res, err := r.db.NewUpdate().Model((*store.Channel)(nil)).Set("config = ?", cfg).Set("updated_at = ?", store.Now()).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return r.Load(ctx)
}

// DeleteBlock 返回渠道不能删除的原因，能删时为空。删除和后台列表共用这条规则。
// 订单和流水靠渠道实例键归属，删掉渠道后历史记录、未完成订单的查单和迟到的回调都没了着落，所以只能停用。
func DeleteBlock(orders, transactions, merchants int64) string {
	switch {
	case orders > 0:
		return "这个渠道已经有订单，只能停用，不能删除"
	case transactions > 0:
		return "这个渠道已经有流水，只能停用，不能删除"
	case merchants > 0:
		return fmt.Sprintf("还有 %d 个商户在用这个渠道，先给它们换渠道", merchants)
	}
	return ""
}

// Delete 删除没有订单、没有流水、没有商户在用的渠道。
func (r *Registry) Delete(ctx context.Context, id int64) error {
	row, err := r.row(ctx, id)
	if err != nil {
		return err
	}
	key := row.Key()
	orders, err := r.db.NewSelect().Model((*store.Order)(nil)).Where("channel = ?", key).Count(ctx)
	if err != nil {
		return err
	}
	txns, err := r.db.NewSelect().Model((*store.Transaction)(nil)).Where("channel = ?", key).Count(ctx)
	if err != nil {
		return err
	}
	merchants, err := r.db.NewSelect().Model((*store.Merchant)(nil)).Where("channel_id = ?", id).Count(ctx)
	if err != nil {
		return err
	}
	if msg := DeleteBlock(orders, txns, merchants); msg != "" {
		return channel.Invalid("%s", msg)
	}
	err = r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewDelete().Model((*store.Channel)(nil)).Where("id = ?", id).Exec(ctx); err != nil {
			return err
		}
		_, err := tx.NewDelete().Model((*store.Setting)(nil)).Where("key = ?", "ledger_state:"+key).Exec(ctx)
		return err
	})
	if err != nil {
		return err
	}
	return r.Load(ctx)
}

func (r *Registry) row(ctx context.Context, id int64) (*store.Channel, error) {
	var row store.Channel
	err := r.db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &row, err
}

// EnsureDemo 在演示模式下、还没有任何渠道时建一个启用的演示渠道，并把没选渠道的商户指向它。
func (r *Registry) EnsureDemo(ctx context.Context) error {
	if !r.Demo {
		return nil
	}
	n, err := r.db.NewSelect().Model((*store.Channel)(nil)).Count(ctx)
	if err != nil || n > 0 {
		return err
	}
	now := store.Now()
	row := &store.Channel{Type: store.ChannelDemo, Name: "演示渠道", Enabled: true, Config: "{}", CreatedAt: now, UpdatedAt: now}
	if _, err := r.db.NewInsert().Model(row).Exec(ctx); err != nil {
		return err
	}
	_, err = r.db.NewUpdate().Model((*store.Merchant)(nil)).Set("channel_id = ?", row.ID).Where("channel_id = 0").Exec(ctx)
	return err
}

// DefaultID 是新建商户默认用的渠道：第一个启用的渠道，没有时用第一个渠道，一个都没有时为 0。
func (r *Registry) DefaultID() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, row := range r.rows {
		if row.Enabled {
			return row.ID
		}
	}
	if len(r.rows) > 0 {
		return r.rows[0].ID
	}
	return 0
}
