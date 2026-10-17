package admin

import (
	"net/http"
	"strconv"
	"strings"

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
	ok(w, map[string]any{"retention": retention, "state": state, "archives": batches})
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
