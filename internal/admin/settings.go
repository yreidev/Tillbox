package admin

import (
	"net/http"
	"strings"

	"tillbox/internal/settings"
	"tillbox/internal/store"
)

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	pay, err := a.Settings.Payment(ctx)
	if err != nil {
		return err
	}
	plat, err := a.Settings.Platform(ctx)
	if err != nil {
		return err
	}
	site, err := a.Settings.Site(ctx)
	if err != nil {
		return err
	}
	ok(w, map[string]any{
		"payment":             pay,
		"platform_public_key": plat.PublicKey,
		"site":                site,
		"demo":                a.Demo,
		"defaults":            settings.DefaultPayment(),
	})
	return nil
}

func (a *API) savePayment(w http.ResponseWriter, r *http.Request) error {
	var p settings.Payment
	if err := readJSON(r, &p); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return badRequest(err.Error())
	}
	if err := a.Settings.SavePayment(r.Context(), p); err != nil {
		return err
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "settings.payment", "", "")
	ok(w, map[string]any{"payment": p})
	return nil
}

func (a *API) regeneratePlatform(w http.ResponseWriter, r *http.Request) error {
	p, err := a.Settings.RegeneratePlatform(r.Context())
	if err != nil {
		return err
	}
	_ = store.Audit(r.Context(), a.DB, a.actor(r), "settings.platform_regenerate", "", "重新生成了平台密钥对")
	ok(w, map[string]any{"platform_public_key": p.PublicKey})
	return nil
}

func (a *API) saveSite(w http.ResponseWriter, r *http.Request) error {
	var in settings.Site
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 32 {
		return badRequest("站点名称不能为空，且不超过 32 个字")
	}
	if err := a.Settings.SaveSite(r.Context(), in); err != nil {
		return err
	}
	ok(w, map[string]any{"site": in})
	return nil
}
