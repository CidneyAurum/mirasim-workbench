package admin

import (
	"net/http"
	"time"

	"mirasim2api/internal/store"
)

// usageTotals 是一个时间窗的用量聚合。
type usageTotals struct {
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	Cost         float64 `json:"cost"`
}

// modelAgg 是按模型聚合的 top 行。
type modelAgg struct {
	Model        string  `json:"model"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	Cost         float64 `json:"cost"`
}

// sumRows 把 store.UsageSummary 行聚合成总量 + 按模型 top（cost 优先、请求数次之，取前 10）。
func sumRows(rows []*store.UsageSummaryRow) (usageTotals, []modelAgg) {
	var t usageTotals
	byModel := map[string]*modelAgg{}
	for _, r := range rows {
		t.Requests += r.Requests
		t.InputTokens += r.InputTokens
		t.OutputTokens += r.OutputTokens
		t.CachedTokens += r.CachedTokens
		t.Cost += r.Cost
		m := byModel[r.Model]
		if m == nil {
			m = &modelAgg{Model: r.Model}
			byModel[r.Model] = m
		}
		m.Requests += r.Requests
		m.InputTokens += r.InputTokens
		m.OutputTokens += r.OutputTokens
		m.CachedTokens += r.CachedTokens
		m.Cost += r.Cost
	}
	top := make([]modelAgg, 0, len(byModel))
	for _, m := range byModel {
		top = append(top, *m)
	}
	for i := 0; i < len(top); i++ {
		for j := i + 1; j < len(top); j++ {
			a, b := top[i], top[j]
			if b.Cost > a.Cost || (b.Cost == a.Cost && b.Requests > a.Requests) {
				top[i], top[j] = top[j], top[i]
			}
		}
	}
	if len(top) > 10 {
		top = top[:10]
	}
	return t, top
}

// handleSummary 仪表盘聚合：账号池状态 + 近 24h / 近 7d 用量 + 模型 top。
func (h *Handler) handleSummary(w http.ResponseWriter, r *http.Request) {
	stats := h.pool.Stats()
	now := time.Now()
	accts := map[string]int{
		"total": len(stats), "enabled": 0, "usable": 0,
		"cooldown": 0, "exhausted": 0, "suspended": 0, "disabled": 0,
	}
	for _, s := range stats {
		if !s.Enabled {
			accts["disabled"]++
			continue
		}
		accts["enabled"]++
		if now.Before(s.CooldownUntil) {
			accts["cooldown"]++
			continue
		}
		if s.Suspended {
			accts["suspended"]++
			continue
		}
		if s.WorstUsedRatio >= 1 {
			accts["exhausted"]++
			continue
		}
		accts["usable"]++
	}

	rows24, err := h.st.UsageSummary(now.Add(-24 * time.Hour))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "用量聚合失败: "+err.Error())
		return
	}
	rows7d, err := h.st.UsageSummary(now.Add(-7 * 24 * time.Hour))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "用量聚合失败: "+err.Error())
		return
	}
	t24, _ := sumRows(rows24)
	t7d, top := sumRows(rows7d)

	writeOK(w, map[string]any{
		"auth_disabled": h.password() == "",
		"accounts":      accts,
		"usage_24h":     t24,
		"usage_7d":      t7d,
		"top_models":    top,
		"generated_at":  now.Unix(),
	})
}
