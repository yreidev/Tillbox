package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"tillbox/internal/backup"
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
	rev, err := a.Settings.Revision(ctx, settings.KeyRetention)
	if err != nil {
		return err
	}
	bk, err := a.backupView(r)
	if err != nil {
		return err
	}
	ok(w, map[string]any{"retention": retention, "retention_revision": rev, "state": state, "archives": batches, "backup": bk})
	return nil
}

type backupView struct {
	Supported bool            `json:"supported"` // 数据库是 SQLite 才有内置备份
	Config    settings.Backup `json:"config"`
	Revision  int64           `json:"revision"`
	State     backup.State    `json:"state"`
	Files     []backup.File   `json:"files"`
	Dir       string          `json:"dir"` // 服务器上的备份目录，恢复时用
}

func (a *API) backupView(r *http.Request) (backupView, error) {
	ctx := r.Context()
	v := backupView{Supported: a.Backup.Supported(), Files: []backup.File{}, Dir: a.Backup.Dir}
	var err error
	if v.Config, err = a.Settings.Backup(ctx); err != nil {
		return v, err
	}
	if v.Revision, err = a.Settings.Revision(ctx, settings.KeyBackup); err != nil {
		return v, err
	}
	if !v.Supported {
		return v, nil
	}
	if v.State, err = a.Backup.Status(ctx); err != nil {
		return v, err
	}
	v.Files, err = a.Backup.List()
	return v, err
}

func (a *API) saveBackup(w http.ResponseWriter, r *http.Request) error {
	var in settings.Backup
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if !a.Backup.Supported() {
		return badRequest(backup.ErrUnsupported.Error())
	}
	old, err := a.Settings.Backup(r.Context())
	if err != nil {
		return err
	}
	rev, err := a.guard(r, []string{settings.KeyBackup}, func() error {
		if err := a.Settings.SaveBackup(r.Context(), in); err != nil {
			return badRequest(err.Error())
		}
		return nil
	})
	if err != nil {
		return err
	}
	detail := fmt.Sprintf("自动备份：%s，保留 %d 份", map[bool]string{true: "开", false: "关"}[in.Enabled], in.Keep)
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "backup.update", "", detail)
	// 关了很久再打开时，到期的话马上备份一份，免得等下一轮之前一直报“超过 26 小时没有成功”。结果记在备份状态里。
	if in.Enabled && !old.Enabled {
		_, _ = a.Backup.Run(r.Context(), false)
	}
	ok(w, map[string]any{"revision": rev})
	return nil
}

func (a *API) runBackup(w http.ResponseWriter, r *http.Request) error {
	if !a.Backup.Supported() {
		return badRequest(backup.ErrUnsupported.Error())
	}
	v, err := a.Backup.Run(r.Context(), true)
	if err != nil {
		if v.LastError != "" {
			return badRequest(v.LastError)
		}
		return err
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "backup.run", v.LastFile, "")
	ok(w, v)
	return nil
}

func (a *API) saveRetention(w http.ResponseWriter, r *http.Request) error {
	var in settings.Retention
	if err := readJSON(r, &in); err != nil {
		return err
	}
	rev, err := a.guard(r, []string{settings.KeyRetention}, func() error {
		if err := a.Settings.SaveRetention(r.Context(), in); err != nil {
			return badRequest(err.Error())
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "retention.update", "", "")
	ok(w, map[string]any{"revision": rev})
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
