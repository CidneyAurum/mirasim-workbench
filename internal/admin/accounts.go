package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mirasim2api/internal/mirasim"
	"mirasim2api/internal/pool"
	"mirasim2api/internal/store"
)

// accountView 是账号在管理 API 中的展示形态。
type accountView struct {
	ID              string  `json:"id"`
	Email           string  `json:"email"`
	Name            string  `json:"name"`
	Provider        string  `json:"provider"`
	Enabled         bool    `json:"enabled"`
	Usable          bool    `json:"usable"`
	Inflight        int64   `json:"inflight"`
	Failures        int     `json:"failures"`
	Cooldown        bool    `json:"cooldown"`
	CooldownSeconds int64   `json:"cooldown_seconds"`
	UsedRatio       float64 `json:"used_ratio"`
	Suspended       bool    `json:"suspended"`
	LimitsFetched   bool    `json:"limits_fetched"`
	LastError       string  `json:"last_error"`
	Plan            string  `json:"plan"`
	PlanExp         int64   `json:"plan_exp"`
}

func statToView(s *pool.AccountStat) accountView {
	now := time.Now()
	cooldown := now.Before(s.CooldownUntil)
	v := accountView{
		ID:            s.ID,
		Email:         s.Email,
		Name:          s.Name,
		Provider:      s.Provider,
		Enabled:       s.Enabled,
		Inflight:      s.Inflight,
		Failures:      s.Failures,
		Cooldown:      cooldown,
		UsedRatio:     s.WorstUsedRatio,
		Suspended:     s.Suspended,
		LimitsFetched: s.LimitsFetched,
		LastError:     s.LastError,
		Plan:          s.Plan,
		PlanExp:       s.PlanExp,
	}
	if cooldown {
		v.CooldownSeconds = int64(time.Until(s.CooldownUntil).Seconds()) + 1
	}
	v.Usable = s.Enabled && !cooldown && !s.Suspended && s.WorstUsedRatio < 1
	return v
}

// handleAccountsList 返回全部账号的运行时状态（来自 pool.Stats）。
func (h *Handler) handleAccountsList(w http.ResponseWriter, _ *http.Request) {
	stats := h.pool.Stats()
	items := make([]accountView, 0, len(stats))
	for _, s := range stats {
		items = append(items, statToView(s))
	}
	writeOK(w, map[string]any{"items": items})
}

// handleAccountPatch 启用/停用账号：{"enabled": bool}。
func (h *Handler) handleAccountPatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.st.GetAccount(id); err != nil {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	if body.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "缺少 enabled 字段")
		return
	}
	if err := h.st.SetAccountEnabled(id, *body.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "更新失败: "+err.Error())
		return
	}
	if err := h.pool.Reload(); err != nil {
		writeErr(w, http.StatusInternalServerError, "账号池重载失败: "+err.Error())
		return
	}
	writeOK(w, map[string]any{"ok": true, "id": id, "enabled": *body.Enabled})
}

// handleAccountDelete 删除账号。
func (h *Handler) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.st.GetAccount(id); err != nil {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	if err := h.st.DeleteAccount(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	if err := h.pool.Reload(); err != nil {
		writeErr(w, http.StatusInternalServerError, "账号池重载失败: "+err.Error())
		return
	}
	writeOK(w, map[string]any{"ok": true, "id": id})
}

// handleAccountRefresh 强制刷新该账号配额（忽略快照保鲜期）。
func (h *Handler) handleAccountRefresh(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry := h.pool.Get(id)
	if entry == nil {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	if err := h.pool.RefreshLimits(r.Context(), entry, true); err != nil {
		writeErr(w, http.StatusBadGateway, "配额刷新失败: "+err.Error())
		return
	}
	for _, s := range h.pool.Stats() {
		if s.ID == id {
			writeOK(w, map[string]any{"ok": true, "account": statToView(s)})
			return
		}
	}
	writeOK(w, map[string]any{"ok": true})
}

// tokenInfo 是从 refresh token JWT 本地解析出的身份/套餐信息。
type tokenInfo struct {
	Email   string
	Name    string
	Plan    string
	PlanExp int64
}

// parseRefreshToken 校验并解析 refresh token（不验签，只做形态/类型/过期检查）。
// 校验规则（PROTOCOL.md）：三段 JWT；token_type≠refresh 报「这是 access token…」；
// exp 已过期报错；身份取 payload 的 email/name。
func parseRefreshToken(token string) (*tokenInfo, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("refresh token 为空")
	}
	if len(strings.Split(token, ".")) != 3 {
		return nil, errors.New("不是合法的 JWT：refresh token 应为三段（header.payload.signature），请确认完整复制")
	}
	claims := mirasim.JWTClaims(token)
	if claims == nil {
		return nil, errors.New("无法解析 JWT payload：请确认粘贴的是 refresh token 原文")
	}
	if tt, _ := claims["token_type"].(string); tt != "" && tt != "refresh" {
		return nil, fmt.Errorf("这是 %s token，不是 refresh token，请改用 refresh token（有效期 30 天的那种）", tt)
	}
	if exp, ok := numericClaim(claims["exp"]); ok && exp > 0 && time.Now().Unix() >= exp {
		return nil, errors.New("refresh token 已过期，请重新登录获取")
	}
	info := &tokenInfo{}
	info.Email, _ = claims["email"].(string)
	if info.Email == "" {
		info.Email, _ = claims["sub"].(string)
	}
	if info.Email == "" {
		return nil, errors.New("JWT 中没有 email 声明，无法识别账号身份")
	}
	info.Name, _ = claims["name"].(string)
	info.Plan, _ = claims["plan"].(string)
	if pe, ok := numericClaim(claims["plan_exp"]); ok {
		info.PlanExp = pe
	}
	return info, nil
}

func numericClaim(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		f, err := n.Int64()
		return f, err == nil
	case string:
		f, err := strconv.ParseInt(n, 10, 64)
		return f, err == nil
	}
	return 0, false
}

// addAccount 校验 refresh token 后入池（store.AddAccount 幂等 + pool.Reload）。
func (h *Handler) addAccount(refreshToken, provider, nameOverride string) (*store.Account, *tokenInfo, error) {
	info, err := parseRefreshToken(refreshToken)
	if err != nil {
		return nil, nil, err
	}
	if provider == "" {
		provider = "manual"
	}
	name := firstNonEmpty(nameOverride, info.Name, info.Email)
	acc, err := h.st.AddAccount(info.Email, name, provider, strings.TrimSpace(refreshToken))
	if err != nil {
		return nil, nil, fmt.Errorf("写入账号失败: %w", err)
	}
	if err := h.pool.Reload(); err != nil {
		return nil, nil, fmt.Errorf("账号池重载失败: %w", err)
	}
	return acc, info, nil
}
