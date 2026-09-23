package admin

import (
	"net/http"
	"strings"
	"time"

	"mirasim2api/internal/store"
)

// keyView 是 API Key 的展示形态（永不包含明文）。
type keyView struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	KeyDisplay        string   `json:"key_display"`
	Enabled           bool     `json:"enabled"`
	Concurrency       int      `json:"concurrency"`
	RateLimitRPM      int      `json:"rate_limit_rpm"`
	ModelAllowlist    []string `json:"model_allowlist"`
	TotalInputTokens  int64    `json:"total_input_tokens"`
	TotalOutputTokens int64    `json:"total_output_tokens"`
	TotalCost         float64  `json:"total_cost"`
	ExpiresAt         int64    `json:"expires_at,omitempty"`
	CreatedAt         int64    `json:"created_at"`
	LastUsedAt        int64    `json:"last_used_at,omitempty"`
}

func keyToView(k *store.APIKey) keyView {
	v := keyView{
		ID:                k.ID,
		Name:              k.Name,
		KeyDisplay:        k.KeyDisplay,
		Enabled:           k.Enabled,
		Concurrency:       k.Concurrency,
		RateLimitRPM:      k.RateLimitRPM,
		ModelAllowlist:    k.ModelAllowlist,
		TotalInputTokens:  k.TotalInputTokens,
		TotalOutputTokens: k.TotalOutputTokens,
		TotalCost:         k.TotalCost,
		CreatedAt:         k.CreatedAt.Unix(),
	}
	if k.ExpiresAt != nil {
		v.ExpiresAt = k.ExpiresAt.Unix()
	}
	if k.LastUsedAt != nil {
		v.LastUsedAt = k.LastUsedAt.Unix()
	}
	return v
}

// keyEditFields 是创建/编辑共用的字段（指针区分「未提供」）。
type keyEditFields struct {
	Name           string    `json:"name"`
	Enabled        *bool     `json:"enabled"`
	Concurrency    *int      `json:"concurrency"`
	RateLimitRPM   *int      `json:"rate_limit_rpm"`
	ModelAllowlist *[]string `json:"model_allowlist"`
	ExpiresAt      *int64    `json:"expires_at"` // unix 秒
}

// validate 返回 (错误消息, ok)；ok=true 表示校验通过（错误消息为空）。
func (f *keyEditFields) validate() (string, bool) {
	if f.Concurrency != nil && (*f.Concurrency < 0 || *f.Concurrency > 1000) {
		return "并发上限须在 0~1000 之间（0=默认 5）", false
	}
	if f.RateLimitRPM != nil && (*f.RateLimitRPM < 0 || *f.RateLimitRPM > 100000) {
		return "RPM 上限须在 0~100000 之间（0=不限）", false
	}
	if f.ModelAllowlist != nil {
		for _, m := range *f.ModelAllowlist {
			if strings.TrimSpace(m) == "" {
				return "模型白名单不能包含空项", false
			}
		}
	}
	return "", true
}

func (h *Handler) handleKeysList(w http.ResponseWriter, _ *http.Request) {
	keys, err := h.st.ListAPIKeys()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取失败: "+err.Error())
		return
	}
	items := make([]keyView, 0, len(keys))
	for _, k := range keys {
		items = append(items, keyToView(k))
	}
	writeOK(w, map[string]any{"items": items})
}

// handleKeyCreate 创建 key，明文只在这一次响应中出现。
func (h *Handler) handleKeyCreate(w http.ResponseWriter, r *http.Request) {
	var body keyEditFields
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	if msg, ok := body.validate(); !ok {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	opts := store.APIKeyOpts{}
	if body.Concurrency != nil {
		opts.Concurrency = *body.Concurrency
	}
	if body.RateLimitRPM != nil {
		opts.RateLimitRPM = *body.RateLimitRPM
	}
	if body.ModelAllowlist != nil {
		opts.ModelAllowlist = *body.ModelAllowlist
	}
	if body.ExpiresAt != nil && *body.ExpiresAt > 0 {
		t := time.Unix(*body.ExpiresAt, 0)
		opts.ExpiresAt = &t
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "未命名"
	}
	plaintext, rec, err := h.st.CreateAPIKey(name, opts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "创建失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":      true,
		"key":     plaintext, // 仅此一次，请立即保存
		"warning": "明文密钥只显示这一次，请立即复制保存",
		"item":    keyToView(rec),
	})
}

func (h *Handler) handleKeyPatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body keyEditFields
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	if msg, ok := body.validate(); !ok {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	u := store.APIKeyUpdate{
		Enabled:        body.Enabled,
		Concurrency:    body.Concurrency,
		RateLimitRPM:   body.RateLimitRPM,
		ModelAllowlist: body.ModelAllowlist,
	}
	if body.ExpiresAt != nil {
		t := time.Unix(*body.ExpiresAt, 0)
		u.ExpiresAt = &t
	}
	if err := h.st.UpdateAPIKey(id, u); err != nil {
		writeErr(w, http.StatusInternalServerError, "更新失败: "+err.Error())
		return
	}
	writeOK(w, map[string]any{"ok": true, "id": id})
}

func (h *Handler) handleKeyDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.st.DeleteAPIKey(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	writeOK(w, map[string]any{"ok": true, "id": id})
}
