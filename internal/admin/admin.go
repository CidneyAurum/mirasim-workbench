// Package admin 实现管理后台：/admin/api/* JSON 接口（HMAC 会话认证）
// 与 /admin/ 内嵌 WebUI 静态资源。
package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"mirasim2api/internal/config"
	"mirasim2api/internal/pool"
	"mirasim2api/internal/runtimecfg"
	"mirasim2api/internal/store"
)

const (
	maxAdminBody = 1 << 20 // 1MB
	// upstreamTimeout 是调认证后端（OAuth/邮箱码）的超时。
	upstreamTimeout = 30 * time.Second
)

// Deps 是 NewHandler 的依赖。Store/Pool/Config 必填。
type Deps struct {
	Store    *store.Store
	Pool     *pool.Pool
	Config   *config.Config
	Settings *runtimecfg.Accessor
	// WebFS 是 WebUI 静态资源（web/ 目录内容）；为 nil 时不挂载 /admin/ 静态。
	WebFS  fs.FS
	Logger *slog.Logger
}

// Handler 是管理后台（API + 静态资源）。
type Handler struct {
	st       *store.Store
	pool     *pool.Pool
	cfg      *config.Config
	settings *runtimecfg.Accessor
	webFS    fs.FS
	logger   *slog.Logger

	sessionKey []byte
	http       *http.Client
	flows      *flowStore
}

// NewHandler 装配管理端路由（Go 1.22 ServeMux，绝对路径含 /admin 前缀）。
func NewHandler(d Deps) http.Handler {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	transport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			raw := ""
			if d.Settings != nil {
				raw = d.Settings.Get(runtimecfg.KeyUpstreamProxy)
			}
			if raw == "" && d.Config != nil {
				raw = d.Config.UpstreamProxy
			}
			if raw == "" {
				return nil, nil
			}
			u, err := url.Parse(raw)
			if err != nil {
				return nil, nil
			}
			return u, nil
		},
	}
	h := &Handler{
		st:         d.Store,
		pool:       d.Pool,
		cfg:        d.Config,
		settings:   d.Settings,
		webFS:      d.WebFS,
		logger:     logger,
		sessionKey: deriveSessionKey(d.Config.MasterKey),
		http:       &http.Client{Transport: transport, Timeout: upstreamTimeout},
		flows:      newFlowStore(),
	}

	mux := http.NewServeMux()
	// 无需会话：登录与登录态查询
	mux.HandleFunc("POST /admin/api/login", h.handleLogin)
	mux.HandleFunc("POST /admin/api/logout", h.handleLogout)
	mux.HandleFunc("GET /admin/api/session", h.handleSessionInfo)
	// 需要会话
	auth := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, h.requireAuth(fn)) }
	auth("GET /admin/api/summary", h.handleSummary)

	auth("GET /admin/api/accounts", h.handleAccountsList)
	auth("GET /admin/api/accounts/oauth/providers", h.handleOAuthProviders)
	auth("POST /admin/api/accounts/oauth/start", h.handleOAuthStart)
	auth("POST /admin/api/accounts/oauth/complete", h.handleOAuthComplete)
	auth("POST /admin/api/accounts/email/start", h.handleEmailStart)
	auth("POST /admin/api/accounts/email/complete", h.handleEmailComplete)
	auth("PATCH /admin/api/accounts/{id}", h.handleAccountPatch)
	auth("DELETE /admin/api/accounts/{id}", h.handleAccountDelete)
	auth("POST /admin/api/accounts/{id}/refresh", h.handleAccountRefresh)

	auth("GET /admin/api/keys", h.handleKeysList)
	auth("POST /admin/api/keys", h.handleKeyCreate)
	auth("PATCH /admin/api/keys/{id}", h.handleKeyPatch)
	auth("DELETE /admin/api/keys/{id}", h.handleKeyDelete)

	auth("GET /admin/api/usage/logs", h.handleUsageLogs)
	auth("GET /admin/api/usage/summary", h.handleUsageSummary)

	auth("GET /admin/api/settings", h.handleSettingsGet)
	auth("PUT /admin/api/settings", h.handleSettingsPut)

	if h.webFS != nil {
		mux.Handle("GET /admin/", http.StripPrefix("/admin/", http.FileServer(http.FS(h.webFS))))
	}
	mux.HandleFunc("/admin/", http.NotFound)
	return mux
}

// password 返回当前管理密码（空表示免密，仅 loopback 部署）。
func (h *Handler) password() string {
	if h.cfg == nil {
		return ""
	}
	return h.cfg.AdminPassword
}

// requireAuth 校验会话 Cookie；未设 ADMIN_PASSWORD 时放行（免密模式）。
func (h *Handler) requireAuth(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.password() == "" {
			fn(w, r)
			return
		}
		if !h.validSession(r) {
			writeErr(w, http.StatusUnauthorized, "登录已过期，请重新登录")
			return
		}
		fn(w, r)
	}
}

// ---------- 通用辅助 ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOK(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// decodeJSON 读取并解析请求体（限制 1MB）。
func decodeJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAdminBody))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func nowUnix() int64 { return time.Now().Unix() }
