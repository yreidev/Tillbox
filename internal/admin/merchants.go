package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"tillbox/internal/merchant"
	"tillbox/internal/store"
)

func (a *API) listMerchants(w http.ResponseWriter, r *http.Request) error {
	ms, err := a.Merchants.List(r.Context())
	if err != nil {
		return err
	}
	// 顺带给出每个商户的订单数，前端用来决定能不能删除。
	var counts []struct {
		MerchantID int64 `bun:"merchant_id"`
		N          int64 `bun:"n"`
	}
	if err := a.DB.NewSelect().Model((*store.Order)(nil)).
		Column("merchant_id").ColumnExpr("COUNT(*) AS n").Group("merchant_id").Scan(r.Context(), &counts); err != nil {
		return err
	}
	byID := map[int64]int64{}
	for _, c := range counts {
		byID[c.MerchantID] = c.N
	}
	items := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		items = append(items, map[string]any{"merchant": m, "orders": byID[m.ID]})
	}
	ok(w, map[string]any{"items": items})
	return nil
}

func merchantErr(err error) error {
	if errors.Is(err, merchant.ErrNotFound) {
		return notFound(err.Error())
	}
	msg := err.Error()
	for _, p := range []string{"商户", "RSA", "只收", "密钥类型", "域名", "备注", "这个商户", "所选"} {
		if strings.HasPrefix(msg, p) {
			return badRequest(msg)
		}
	}
	return err
}

func (a *API) createMerchant(w http.ResponseWriter, r *http.Request) error {
	var in merchant.Input
	if err := readJSON(r, &in); err != nil {
		return err
	}
	m, key, err := a.Merchants.Create(r.Context(), in)
	if err != nil {
		return merchantErr(err)
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "merchant.create", fmt.Sprint(m.ID), m.Name)
	ok(w, map[string]any{"merchant": m, "key": key})
	return nil
}

func (a *API) updateMerchant(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in merchant.Input
	if err := readJSON(r, &in); err != nil {
		return err
	}
	m, err := a.Merchants.Update(r.Context(), id, in)
	if err != nil {
		return merchantErr(err)
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "merchant.update", fmt.Sprint(id), fmt.Sprintf("名称 %s，启用 %v，密钥类型 %d", m.Name, m.Enabled, m.KeyType))
	ok(w, map[string]any{"merchant": m})
	return nil
}

func (a *API) deleteMerchant(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := a.Merchants.Delete(r.Context(), id); err != nil {
		return merchantErr(err)
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "merchant.delete", fmt.Sprint(id), "")
	ok(w, map[string]any{})
	return nil
}

func (a *API) merchantKey(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	m, err := a.Merchants.Get(r.Context(), id)
	if err != nil {
		return merchantErr(err)
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "merchant.view_key", fmt.Sprint(id), "查看了 MD5 密钥")
	ok(w, map[string]any{"key": m.Key})
	return nil
}

func (a *API) resetMerchantKey(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	key, err := a.Merchants.ResetKey(r.Context(), id)
	if err != nil {
		return merchantErr(err)
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "merchant.reset_key", fmt.Sprint(id), "重置了 MD5 密钥")
	ok(w, map[string]any{"key": key})
	return nil
}
