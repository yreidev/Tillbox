package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"tillbox/internal/store"
)

// CSRFHeader 是修改类请求必须带的请求头。跨站表单带不上自定义头，配合 SameSite=Strict 的 cookie 防 CSRF。
const CSRFHeader = "X-Tillbox"

type ctxKey struct{}

// Current 返回当前登录的管理员。
func Current(ctx context.Context) *store.Admin {
	ad, _ := ctx.Value(ctxKey{}).(*store.Admin)
	return ad
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ok(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }

// httpError 是带状态码的错误，消息直接给前端看。
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(msg string) error { return &httpError{http.StatusBadRequest, msg} }
func notFound(msg string) error   { return &httpError{http.StatusNotFound, msg} }

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var he *httpError
	if errors.As(err, &he) {
		writeJSON(w, he.status, map[string]string{"error": he.msg})
		return
	}
	slog.Error("后台接口出错", "path", r.URL.Path, "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "服务器出错：" + err.Error()})
}

func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return badRequest("读取请求失败")
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return badRequest("请求格式不对：" + err.Error())
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("编号不对")
	}
	return id, nil
}

func queryInt(r *http.Request, name string, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	return n
}

func queryInt64(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return n
}

// page 读分页参数，返回 limit 和 offset。
func page(r *http.Request) (int, int) {
	size := min(max(queryInt(r, "page_size", 20), 1), 200)
	p := max(queryInt(r, "page", 1), 1)
	return size, (p - 1) * size
}

type handler func(w http.ResponseWriter, r *http.Request) error

// wrap 把返回 error 的处理函数转成 http.HandlerFunc，并统一处理鉴权和 CSRF。
func (a *API) wrap(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(CSRFHeader) == "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "缺少请求头 " + CSRFHeader})
			return
		}
		c, _ := r.Cookie(CookieName)
		token := ""
		if c != nil {
			token = c.Value
		}
		ad, err := a.Auth.Session(r.Context(), token)
		if err != nil {
			if errors.Is(err, ErrNoSession) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
				return
			}
			writeError(w, r, err)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, ad))
		if err := h(w, r); err != nil {
			writeError(w, r, err)
		}
	}
}

// open 是不需要登录的接口（登录本身），仍然检查 CSRF 头。
func (a *API) open(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get(CSRFHeader) == "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "缺少请求头 " + CSRFHeader})
			return
		}
		if err := h(w, r); err != nil {
			writeError(w, r, err)
		}
	}
}
