package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"tillbox/internal/channel"
	"tillbox/internal/channel/alipayqr"
	"tillbox/internal/channel/registry"
	"tillbox/internal/channel/upstream"
	"tillbox/internal/confirm"
	"tillbox/internal/ledger"
	"tillbox/internal/store"
)

// channelView 是后台渠道列表的一项。配置按类型给出，不含密钥。
type channelView struct {
	store.Channel
	Key          string          `json:"key"`
	Config       any             `json:"config"`
	Problem      string          `json:"problem"` // 不能收款的原因，能收款时为空
	PayTypes     []string        `json:"pay_types"`
	Orders       int64           `json:"orders"`
	Transactions int64           `json:"transactions"`
	Merchants    int64           `json:"merchants"`
	DeleteBlock  string          `json:"delete_block"`       // 不能删除的原因，能删时为空
	Ledger       *ledger.Status  `json:"ledger,omitempty"`   // 推断型渠道的查账状态
	Upstream     *confirm.Status `json:"upstream,omitempty"` // 上游渠道的预下单、查单和回调状态
}

func channelErr(err error) error {
	var inv *channel.InvalidError
	switch {
	case errors.As(err, &inv):
		return badRequest(inv.Msg)
	case errors.Is(err, registry.ErrNotFound):
		return notFound(err.Error())
	}
	return err
}

func configView(row store.Channel) any {
	switch row.Type {
	case store.ChannelAlipayQR:
		return alipayqr.ViewOf(row.Config)
	case store.ChannelEpay:
		return upstream.ViewOf(row.Config)
	}
	return map[string]any{}
}

func (a *API) channelViews(ctx context.Context) ([]channelView, error) {
	var orders, txns, merchants []struct {
		Key string `bun:"k"`
		N   int64  `bun:"n"`
	}
	if err := a.DB.NewSelect().Model((*store.Order)(nil)).ColumnExpr("channel AS k").ColumnExpr("COUNT(*) AS n").Group("channel").Scan(ctx, &orders); err != nil {
		return nil, err
	}
	if err := a.DB.NewSelect().Model((*store.Transaction)(nil)).ColumnExpr("channel AS k").ColumnExpr("COUNT(*) AS n").Group("channel").Scan(ctx, &txns); err != nil {
		return nil, err
	}
	if err := a.DB.NewSelect().Model((*store.Merchant)(nil)).ColumnExpr("'ch' || CAST(channel_id AS TEXT) AS k").ColumnExpr("COUNT(*) AS n").Group("channel_id").Scan(ctx, &merchants); err != nil {
		return nil, err
	}
	count := func(rows []struct {
		Key string `bun:"k"`
		N   int64  `bun:"n"`
	}, key string) int64 {
		for _, r := range rows {
			if r.Key == key {
				return r.N
			}
		}
		return 0
	}
	out := make([]channelView, 0)
	for _, row := range a.Channels.List() {
		v := channelView{Channel: row, Key: row.Key(), Config: configView(row),
			Orders: count(orders, row.Key()), Transactions: count(txns, row.Key()), Merchants: count(merchants, row.Key())}
		v.DeleteBlock = registry.DeleteBlock(v.Orders, v.Transactions, v.Merchants)
		if inst, _, ok := a.Channels.Get(row.ID); ok {
			v.PayTypes = inst.PayTypes()
			if err := inst.Ready(ctx); err != nil {
				v.Problem = err.Error()
			}
		}
		if p := a.Pollers.Get(row.Key()); p != nil {
			st := p.Status()
			v.Ledger = &st
		}
		if row.Type == store.ChannelEpay {
			st := a.Confirm.Status(row.Key())
			v.Upstream = &st
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *API) listChannels(w http.ResponseWriter, r *http.Request) error {
	items, err := a.channelViews(r.Context())
	if err != nil {
		return err
	}
	ok(w, map[string]any{"items": items, "demo": a.Demo})
	return nil
}

func (a *API) createChannel(w http.ResponseWriter, r *http.Request) error {
	var in registry.Input
	if err := readJSON(r, &in); err != nil {
		return err
	}
	row, err := a.Channels.Create(r.Context(), in)
	if err != nil {
		return channelErr(err)
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "channel.create", fmt.Sprint(row.ID), fmt.Sprintf("%s（%s）", row.Name, row.Type))
	ok(w, map[string]any{"channel": row})
	return nil
}

func (a *API) updateChannel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in registry.Input
	if err := readJSON(r, &in); err != nil {
		return err
	}
	row, err := a.Channels.Update(r.Context(), id, in)
	if err != nil {
		return channelErr(err)
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "channel.update", fmt.Sprint(id), fmt.Sprintf("名称 %s，启用 %v", row.Name, row.Enabled))
	_ = a.Pollers.ResetBackoff(context.WithoutCancel(r.Context()), row.Key())
	ok(w, map[string]any{"channel": row})
	return nil
}

func (a *API) deleteChannel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := a.Channels.Delete(r.Context(), id); err != nil {
		return channelErr(err)
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "channel.delete", fmt.Sprint(id), "")
	ok(w, map[string]any{})
	return nil
}

// channelKeypair 生成新的 RSA 密钥对：私钥加密存库，公钥返回给用户。
// 经营码渠道上传到支付宝开放平台（应用公钥）；上游易支付 V2 填到上游的商户后台（商户公钥）。
func (a *API) channelKeypair(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	_, row, found := a.Channels.Get(id)
	if !found {
		return notFound(registry.ErrNotFound.Error())
	}
	var cfg, pub string
	switch row.Type {
	case store.ChannelAlipayQR:
		cfg, pub, err = alipayqr.Keypair(a.Settings.Box(), row.Config)
	case store.ChannelEpay:
		cfg, pub, err = upstream.Keypair(a.Settings.Box(), row.Config)
	default:
		return badRequest("这个渠道不需要密钥对")
	}
	if err != nil {
		return err
	}
	if err := a.Channels.SaveConfig(r.Context(), id, cfg); err != nil {
		return channelErr(err)
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "channel.keypair", fmt.Sprint(id), "生成了新的密钥对")
	ok(w, map[string]any{"public_key": pub})
	return nil
}

func (a *API) testChannel(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	inst, row, found := a.Channels.Get(id)
	if !found {
		return notFound(registry.ErrNotFound.Error())
	}
	t, can := inst.(channel.Tester)
	if !can {
		return badRequest("这个渠道不支持测试连接")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err = t.Test(ctx)
	if err == nil {
		msg := "连接正常"
		if row.Type == store.ChannelAlipayQR {
			msg = "连接正常，账务明细查询权限可用"
		}
		ok(w, map[string]any{"ok": true, "message": msg})
		return nil
	}
	msg := err.Error()
	switch {
	case errors.Is(err, channel.ErrNotConfigured) && row.Type == store.ChannelAlipayQR:
		msg = "还没有填完应用 ID、应用密钥和支付宝公钥"
	case errors.Is(err, channel.ErrAuth) && row.Type == store.ChannelAlipayQR:
		msg = "凭据或权限有问题：" + msg + "。请确认应用已上线、已开通账务明细查询权限，应用公钥已上传、支付宝公钥填对了"
	}
	ok(w, map[string]any{"ok": false, "message": msg})
	return nil
}
