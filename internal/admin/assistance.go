package admin

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"tillbox/internal/settings"
	"tillbox/internal/store"
)

func (a *API) assistance(w http.ResponseWriter, r *http.Request) error {
	cfg, err := a.Settings.Automation(r.Context())
	if err != nil {
		return err
	}
	hasKey := cfg.APIKey != ""
	cfg.APIKey = ""
	state, err := a.Assistance.Status(r.Context())
	if err != nil {
		return err
	}
	limit, offset := page(r)
	cs := make([]store.AssistanceCase, 0)
	q := a.DB.NewSelect().Model(&cs)
	if status := r.URL.Query().Get("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	total, err := q.Order("ac.id DESC").Limit(int64(limit)).Offset(int64(offset)).ScanAndCount(r.Context())
	if err != nil {
		return err
	}
	counts := map[string]int64{}
	var rows []struct {
		Status string
		N      int64
	}
	if err = a.DB.NewSelect().Model((*store.AssistanceCase)(nil)).Column("status").ColumnExpr("COUNT(*) AS n").Group("status").Scan(r.Context(), &rows); err != nil {
		return err
	}
	for _, row := range rows {
		counts[row.Status] = row.N
	}
	ok(w, map[string]any{"config": cfg, "has_api_key": hasKey, "state": state, "items": cs, "total": total, "counts": counts})
	return nil
}
func (a *API) saveAssistance(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		settings.Automation
		ClearKey bool `json:"clear_key"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	cur, err := a.Settings.Automation(r.Context())
	if err != nil {
		return err
	}
	if in.APIKey == "" && !in.ClearKey {
		old, _ := url.Parse(cur.BaseURL)
		next, _ := url.Parse(in.BaseURL)
		if cur.APIKey != "" && (next == nil || old == nil || !strings.EqualFold(old.Host, next.Host) || old.Scheme != next.Scheme) {
			return badRequest("更换模型接口域名时请重新填写 API Key，或勾选清除密钥")
		}
		in.APIKey = cur.APIKey
	}
	if in.ClearKey {
		in.APIKey = ""
	}
	if err = a.Assistance.Configure(r.Context(), in.Automation); err != nil {
		return badRequest(err.Error())
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "assistance.config", "", "更新模型配置及自动操作范围；不记录密钥")
	a.Assistance.Wake()
	ok(w, map[string]any{})
	return nil
}
func (a *API) testAssistance(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.Assistance.Test(r.Context()); err != nil {
		return badRequest(err.Error())
	}
	ok(w, map[string]string{"message": "模型连接和动作 JSON 校验通过"})
	return nil
}
func (a *API) runAssistance(w http.ResponseWriter, r *http.Request) error {
	cfg, err := a.Settings.Automation(r.Context())
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return badRequest("请先配置模型并启用自动处理")
	}
	a.Assistance.Wake()
	ok(w, map[string]string{"message": "巡检请求已提交，将按配置间隔执行"})
	return nil
}
func (a *API) assistanceDetail(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var c store.AssistanceCase
	if err = a.DB.NewSelect().Model(&c).Where("id = ?", id).Scan(r.Context()); err != nil {
		return notFound("处理记录不存在")
	}
	actions := make([]store.AssistanceAction, 0)
	if err = a.DB.NewSelect().Model(&actions).Where("case_id = ?", id).Order("id DESC").Limit(100).Scan(r.Context()); err != nil {
		return err
	}
	ok(w, map[string]any{"case": c, "actions": actions})
	return nil
}
func (a *API) updateAssistance(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Action  string `json:"action"`
		Note    string `json:"note"`
		TradeNo string `json:"trade_no"`
	}
	if err = readJSON(r, &in); err != nil {
		return err
	}
	if in.Action != "retry" && in.Action != "resolve" && in.Action != "associate" {
		return badRequest("操作不正确")
	}
	if len([]rune(strings.TrimSpace(in.Note))) < 2 || len([]rune(in.Note)) > 1000 {
		return badRequest("请填写 2–1000 字的处理说明")
	}
	if in.Action == "associate" {
		err = a.Assistance.AssociateCase(r.Context(), id, strings.TrimSpace(in.TradeNo), in.Note)
	} else {
		err = a.Assistance.UpdateCase(r.Context(), id, in.Action, in.Note)
	}
	if err != nil {
		return badRequest(err.Error())
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "assistance."+in.Action, strconv.FormatInt(id, 10), in.Note)
	ok(w, map[string]any{})
	return nil
}
