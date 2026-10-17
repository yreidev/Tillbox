package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"tillbox/internal/settings"
	"tillbox/internal/store"
)

func (a *API) operations(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	state, err := a.Maintenance.Status(ctx)
	if err != nil {
		return err
	}
	retention, err := a.Settings.Retention(ctx)
	if err != nil {
		return err
	}
	batches := make([]store.ArchiveBatch, 0)
	if err := a.DB.NewSelect().Model(&batches).Order("id DESC").Limit(50).Scan(ctx); err != nil {
		return err
	}
	issues := make([]string, 0)
	pay, err := a.Settings.Payment(ctx)
	if err != nil {
		return err
	}
	// 停用的经营码渠道不查账，和宿主机巡检一样不算问题。
	for _, st := range a.enabledLedgers() {
		if st.Failures >= 3 {
			issues = append(issues, "渠道「"+st.Name+"」主窗口查账连续失败")
		}
		if st.BackfillFailures >= 3 {
			issues = append(issues, "渠道「"+st.Name+"」历史补扫连续失败")
		}
		if st.LastOKAt > 0 && time.Since(store.Time(st.LastOKAt)) > max(5*time.Minute, time.Duration(pay.NormalInterval)*3*time.Second) {
			issues = append(issues, "渠道「"+st.Name+"」查账成功时间超过阈值")
		}
	}
	for _, row := range a.Channels.List() {
		if row.Type != store.ChannelEpay {
			continue
		}
		st := a.Confirm.Status(row.Key())
		if st.Failures >= 3 {
			last := st.CheckErr
			if st.PrepayErrAt > st.CheckErrAt {
				last = st.PrepayErr
			}
			issues = append(issues, "渠道「"+row.Name+"」上游预下单或查单连续失败："+last)
		}
		if st.NotifyErrAt > st.NotifyAt && time.Since(store.Time(st.NotifyErrAt)) < 24*time.Hour {
			issues = append(issues, "渠道「"+row.Name+"」最近收到不合格的回调："+st.NotifyErr)
		}
	}
	dead, err := a.DB.NewSelect().Model((*store.Notification)(nil)).Where("status = ?", store.NotifyDead).Count(ctx)
	if err != nil {
		return err
	}
	if dead > 0 {
		issues = append(issues, "存在通知死信："+strconv.FormatInt(dead, 10)+" 笔")
	}
	aiConfig, err := a.Settings.Automation(ctx)
	if err != nil {
		return err
	}
	aiState, err := a.Assistance.Status(ctx)
	if err != nil {
		return err
	}
	if aiConfig.Enabled && (aiState.LastError != "" || (aiState.LastRunAt > 0 && time.Since(store.Time(aiState.LastRunAt)) > max(10*time.Minute, time.Duration(aiConfig.IntervalSeconds)*3*time.Second))) {
		issues = append(issues, "AI 自动处理巡检异常，请查看 AI 与付款申诉")
	}
	manual, err := a.DB.NewSelect().Model((*store.AssistanceCase)(nil)).Where("status = ?", "manual").Count(ctx)
	if err != nil {
		return err
	}
	if manual > 0 {
		issues = append(issues, "待人工处理的付款异常或申诉："+strconv.FormatInt(manual, 10)+" 项")
	}
	if state.LastError != "" {
		issues = append(issues, state.LastError)
	}
	if retention.Enabled && state.LastOKAt > 0 && time.Since(store.Time(state.LastOKAt)) > 48*time.Hour {
		issues = append(issues, "归档任务超过 48 小时无成功执行")
	}
	ok(w, map[string]any{"issues": issues, "retention": retention, "state": state, "archives": batches})
	return nil
}

func (a *API) saveRetention(w http.ResponseWriter, r *http.Request) error {
	var in settings.Retention
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := a.Settings.SaveRetention(r.Context(), in); err != nil {
		return badRequest(err.Error())
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "retention.update", "", "")
	ok(w, map[string]any{})
	return nil
}

func (a *API) runArchive(w http.ResponseWriter, r *http.Request) error {
	v, err := a.Maintenance.Run(r.Context(), true)
	if err != nil {
		return badRequest(v.LastError)
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "archive.run", "", "")
	ok(w, v)
	return nil
}

func (a *API) downloadArchive(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	data, batch, err := a.Maintenance.Read(r.Context(), id)
	if err != nil {
		return badRequest("归档不存在、校验失败或主密钥不匹配")
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "archive.download", strconv.FormatInt(id, 10), "")
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.TrimSuffix(batch.Filename, ".enc")+`"`)
	_, err = w.Write(data)
	return err
}
