package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"mirasim2api/internal/billing"
	"mirasim2api/internal/config"
	"mirasim2api/internal/pool"
	"mirasim2api/internal/runtimecfg"
	"mirasim2api/internal/store"
)

const maxBodyBytes = 8 << 20 // 8MB

// Deps 是 NewHandler 的依赖。Store/Pool/Config 必填；
// Billing 为 nil 时跳过计费；HTTPClient 为 nil 时按运行时设置/Config.UpstreamProxy 构造。
type Deps struct {
	Store   *store.Store
	Pool    *pool.Pool
	Config  *config.Config
	Billing *billing.Recorder

	// Settings 读取运行时设置（管理端可热改，键见 internal/runtimecfg）；
	// 为 nil 时退回 Config 的环境变量值（阶段 2 行为）。
	Settings func(key string) string
	// HTTPClient 执行上游模型请求（不带超时，流式依赖请求 ctx 取消）。
	HTTPClient *http.Client
	// KeySlotTimeout 等待 key 并发槽的超时（默认 30s）。
	KeySlotTimeout time.Duration
	// Logger 请求日志输出（默认 slog.Default()）。
	Logger *slog.Logger
}

// Handler 是模型网关，可直接作为 http.Handler 挂载（阶段 3 管理端可同 mux 挂载）。
type Handler struct {
	st      *store.Store
	pool    *pool.Pool
	cfg     *config.Config
	billing *billing.Recorder
	http    *http.Client
	logger  *slog.Logger

	settingsFn     func(key string) string
	slots          *keySlotter
	rates          *rateLimiter
	keySlotTimeout time.Duration
}

// NewHandler 装配网关路由（Go 1.22 ServeMux）。
func NewHandler(d Deps) http.Handler {
	httpClient := d.HTTPClient
	if httpClient == nil {
		// 代理每次请求动态求值：运行时设置（数据库）优先，Config 环境变量兜底。
		transport := &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				raw := ""
				if d.Settings != nil {
					raw = d.Settings(runtimecfg.KeyUpstreamProxy)
				}
				if raw == "" && d.Config != nil {
					raw = d.Config.UpstreamProxy
				}
				if raw == "" {
					return nil, nil
				}
				u, err := url.Parse(raw)
				if err != nil {
					return nil, nil // 非法代理地址视同直连（管理端写入时已校验）
				}
				return u, nil
			},
		}
		httpClient = &http.Client{Transport: transport} // 无超时：流式靠 ctx
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{
		st:             d.Store,
		pool:           d.Pool,
		cfg:            d.Config,
		billing:        d.Billing,
		http:           httpClient,
		logger:         logger,
		settingsFn:     d.Settings,
		slots:          newKeySlotter(),
		rates:          newRateLimiter(),
		keySlotTimeout: d.KeySlotTimeout,
	}
	mux := http.NewServeMux()
	for _, prefix := range []string{"/v1", ""} {
		mux.HandleFunc("POST "+prefix+"/messages", h.handleModel(epMessages))
		mux.HandleFunc("POST "+prefix+"/responses", h.handleModel(epResponses))
		mux.HandleFunc("POST "+prefix+"/chat/completions", h.handleModel(epChat))
	}
	mux.HandleFunc("GET /v1/models", h.handleModels)
	mux.HandleFunc("GET /health", h.handleHealth)
	return mux
}

// staticModels 是池无可用账号时 /v1/models 的内置清单。
var staticModels = []string{
	"claude-opus-5", "claude-sonnet-5", "claude-fable-5", "claude-haiku-4-5",
	"claude-opus-4-8", "gpt-6-astra", "kimi-k3", "deepseek-flash",
	"deepseek-v4-flash", "glm-5.3-flash",
}

// handleModel 返回某端点的处理函数：读体 → 规范化 → 家族路由校验 →
// 鉴权 → 并发/限流 → 转发。
func (h *Handler) handleModel(endpoint string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		if err != nil {
			writeError(w, endpoint, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
			return
		}
		cloakMode := "relaxed"
		if h.cfg != nil && h.cfg.ClaudeCloakMode != "" {
			cloakMode = h.cfg.ClaudeCloakMode
		}
		if v := h.runtimeSetting(runtimecfg.KeyCloakMode); v == "relaxed" || v == "strict" {
			cloakMode = v // 运行时设置优先
		}
		norm, err := normalize(endpoint, raw, cloakMode)
		if err != nil {
			writeError(w, endpoint, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		// 模型家族路由（PROTOCOL.md）：claude-* 只能 /v1/messages，
		// gpt-* 只能 /v1/responses，第三方三个端点都行。
		switch modelFamily(norm.Model) {
		case familyClaude:
			if endpoint != epMessages {
				writeError(w, endpoint, http.StatusBadRequest, "invalid_request_error",
					"model "+norm.Model+" is only available at /v1/messages")
				return
			}
		case familyGPT:
			if endpoint != epResponses {
				writeError(w, endpoint, http.StatusBadRequest, "invalid_request_error",
					"model "+norm.Model+" is only available at /v1/responses")
				return
			}
		}

		key, ok := h.authenticate(w, r, endpoint, norm.Model)
		if !ok {
			return
		}
		release := h.acquireKeyLimits(w, r, endpoint, key)
		if release == nil {
			return
		}
		defer release()
		if key != nil {
			defer func() { go h.st.TouchAPIKeyUsed(key.ID) }()
		}
		h.forward(w, r, endpoint, norm, key)
	}
}

// runtimeSetting 读运行时设置；未注入或键未设置时返回空串。
func (h *Handler) runtimeSetting(key string) string {
	if h.settingsFn == nil {
		return ""
	}
	return h.settingsFn(key)
}

// handleModels 透传 relay /v1/models（与 /v1/limits 同款签名请求）；
// 池无可用账号或上游失败时返回内置静态清单（OpenAI models 格式）。
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	key, ok := h.authenticate(w, r, epChat, "")
	if !ok {
		return
	}
	_ = key
	if allowed := h.pool.ModelAllowlist(); allowed != nil {
		data := make([]map[string]any, 0, len(allowed))
		for _, id := range allowed {
			data = append(data, map[string]any{"id": id, "object": "model", "created": 0, "owned_by": "mirasim"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = encodeJSON(w, map[string]any{"object": "list", "data": data, "source": "plan:go"})
		return
	}
	if entry := h.pool.Pick(nil, ""); entry != nil {
		if req, err := entry.Client.SignedRequest(r.Context(), http.MethodGet, "/v1/models", nil); err == nil {
			if resp, err := h.http.Do(req); err == nil {
				defer resp.Body.Close()
				body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
				if err == nil && resp.StatusCode == http.StatusOK {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(body)
					return
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	data := make([]map[string]any, 0, len(staticModels))
	for _, id := range staticModels {
		data = append(data, map[string]any{
			"id": id, "object": "model", "created": 0, "owned_by": "mirasim",
		})
	}
	_ = encodeJSON(w, map[string]any{"object": "list", "data": data})
}

// handleHealth 无需鉴权，返回账号池摘要。
func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	stats := h.pool.Stats()
	available := 0
	for _, s := range stats {
		if s.Enabled && time.Now().After(s.CooldownUntil) && !s.Suspended && s.WorstUsedRatio < 1 {
			available++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = encodeJSON(w, map[string]any{
		"status":    "ok",
		"accounts":  len(stats),
		"available": available,
	})
}

func encodeJSON(w http.ResponseWriter, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}
