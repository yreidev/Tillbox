package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"tillbox/internal/confirm"
)

// channelNotify 接收上游易支付的支付结果回调（官方用 GET，也接受 POST 表单）。
// 处理好了回 success；不合格的回调回 fail；本系统出错时回 500，让上游稍后重试。
func channelNotify(cf *confirm.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("fail"))
			return
		}
		p := map[string]string{}
		for k, v := range r.URL.Query() {
			p[k] = v[0]
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if err := r.ParseForm(); err == nil {
				for k, v := range r.PostForm {
					if _, ok := p[k]; !ok {
						p[k] = v[0]
					}
				}
			}
		}
		err = cf.Notify(r.Context(), id, p)
		var bad *confirm.BadNotify
		switch {
		case err == nil:
			_, _ = w.Write([]byte("success"))
		case errors.As(err, &bad):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("fail"))
		default:
			slog.Error("处理上游回调失败", "channel_id", id, "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("fail"))
		}
	}
}
