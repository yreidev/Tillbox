package assistance

import (
	"context"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	"tillbox/internal/settings"
	"tillbox/internal/store"
)

// Configure serializes permission changes with executing actions. Once a disable
// request returns, no previously proposed action can begin executing.
func (s *Service) Configure(ctx context.Context, cfg settings.Automation) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	return s.Settings.SaveAutomation(ctx, cfg)
}

// AssociateCase is an administrator's explicit confirmation of claim ownership.
// It associates only the support case, never the transaction or paid state.
func (s *Service) AssociateCase(ctx context.Context, id int64, tradeNo, note string) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	var c store.AssistanceCase
	if err := s.DB.NewSelect().Model(&c).Where("id = ?", id).Scan(ctx); err != nil {
		return errors.New("处理记录不存在")
	}
	if c.Source != "payer" || c.OrderID != 0 || c.Status == "resolved" {
		return errors.New("仅未关联且未结案的付款人申诉可以关联订单")
	}
	o, err := s.Orders.ByTradeNo(ctx, tradeNo)
	if err != nil {
		return errors.New("平台订单号不存在")
	}
	c.OrderID = o.ID
	c.Status = "manual"
	c.Summary = note
	c.PublicReply = "处理人员已关联订单，正在核查付款情况。"
	c.UpdatedAt = max(store.Now(), c.UpdatedAt+1)
	return s.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model(&c).Column("order_id", "status", "summary", "public_reply", "updated_at").WherePK().Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewInsert().Model(&store.AssistanceAction{CaseID: id, Action: "admin_associate", Reason: note, Result: "人工确认申诉关联订单 " + tradeNo + "；不改变账务状态", CreatedAt: store.Now()}).Exec(ctx); err != nil {
			return err
		}
		return store.Audit(ctx, tx, "管理员", "assistance.associate", fmt.Sprint(id), "订单 "+tradeNo+"；"+note)
	})
}

func (s *Service) UpdateCase(ctx context.Context, id int64, action, note string) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	var c store.AssistanceCase
	if err := s.DB.NewSelect().Model(&c).Where("id = ?", id).Scan(ctx); err != nil {
		return errors.New("处理记录不存在")
	}
	c.Status = "resolved"
	c.PublicReply = note
	if action == "retry" {
		c.Status = "pending"
		c.Attempts = 0
		c.NextAt = store.Now()
		c.PublicReply = "已重新提交自动核查。"
	}
	c.Summary = note
	c.UpdatedAt = max(store.Now(), c.UpdatedAt+1)
	err := s.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model(&c).Column("status", "public_reply", "summary", "attempts", "next_at", "updated_at").WherePK().Exec(ctx); err != nil {
			return err
		}
		// A ledger case is the anomaly itself: closing one closes the other, so
		// the same problem is not left open on the ledger page.
		if c.Status == "resolved" && c.Source == "ledger" && c.AnomalyID > 0 {
			res, err := tx.NewUpdate().Model((*store.Anomaly)(nil)).
				Set("status = ?", store.AnomalyResolved).Set("resolved_at = ?", store.Now()).
				Set("resolved_by = ?", "管理员").Set("note = ?", note).
				Where("id = ?", c.AnomalyID).Where("status = ?", store.AnomalyOpen).Exec(ctx)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				if err := store.Audit(ctx, tx, "管理员", "anomaly.resolve", fmt.Sprint(c.AnomalyID), note); err != nil {
					return err
				}
			}
		}
		_, err := tx.NewInsert().Model(&store.AssistanceAction{CaseID: id, Action: "admin_" + action, Reason: note, Result: "管理员操作；不改变订单账务状态", CreatedAt: store.Now()}).Exec(ctx)
		return err
	})
	if err == nil {
		s.Wake()
	}
	return err
}
