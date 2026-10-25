package admin

import (
	"net/http"

	"tillbox/internal/settings"
	"tillbox/internal/store"
)

func (a *API) getUpdate(w http.ResponseWriter, r *http.Request) error {
	v, err := a.Update.View(r.Context())
	if err != nil {
		return err
	}
	ok(w, v)
	return nil
}

func (a *API) saveUpdate(w http.ResponseWriter, r *http.Request) error {
	var in settings.Update
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if _, err := a.guard(r, []string{settings.KeyUpdate}, func() error { return a.Settings.SaveUpdate(r.Context(), in) }); err != nil {
		return err
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "settings.update", "", map[bool]string{true: "开启了新版本提醒", false: "关闭了新版本提醒"}[in.Check])
	return a.getUpdate(w, r)
}

// checkUpdate 马上向 GitHub 查一次，关着定时检查时也能手动查。
func (a *API) checkUpdate(w http.ResponseWriter, r *http.Request) error {
	if st, err := a.Update.Check(r.Context()); err != nil {
		if st.Error != "" {
			return badRequest(st.Error)
		}
		return err
	}
	return a.getUpdate(w, r)
}
