package admin

import (
	"net/http"
	"strconv"
	"time"

	"mirasim2api/internal/store"
)

// parseSince 解析 since 参数：RFC3339 / unix 秒 / Go duration（如 "24h"，表示距今）。
func parseSince(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(n, 0), nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, strconv.ErrSyntax
}

// usageLogView 是一条用量日志的展示形态。
type usageLogView struct {
	ID           int64   `json:"id"`
	APIKeyID     string  `json:"api_key_id"`
	AccountID    string  `json:"account_id"`
	Model        string  `json:"model"`
	Endpoint     string  `json:"endpoint"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	Cost         float64 `json:"cost"`
	Status       int     `json:"status"`
	Err          string  `json:"err"`
	DurationMs   int64   `json:"duration_ms"`
	CreatedAt    int64   `json:"created_at"`
}

func logToView(l *store.UsageLog) usageLogView {
	return usageLogView{
		ID: l.ID, APIKeyID: l.APIKeyID, AccountID: l.AccountID, Model: l.Model,
		Endpoint: l.Endpoint, InputTokens: l.InputTokens, OutputTokens: l.OutputTokens,
		CachedTokens: l.CachedTokens, Cost: l.Cost, Status: l.Status, Err: l.Err,
		DurationMs: l.DurationMs, CreatedAt: l.CreatedAt.Unix(),
	}
}

// handleUsageLogs 分页查询用量日志：
// ?keyId=&accountId=&model=&since=&limit=&offset=
func (h *Handler) handleUsageLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since, err := parseSince(q.Get("since"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "since 参数无法解析（支持 RFC3339 / unix 秒 / 24h 这类时长）")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	f := store.UsageLogFilter{
		KeyID:     q.Get("keyId"),
		AccountID: q.Get("accountId"),
		Model:     q.Get("model"),
		Since:     since,
		Limit:     limit,
		Offset:    offset,
	}
	logs, err := h.st.QueryUsageLogs(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	total, err := h.st.CountUsageLogs(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "统计失败: "+err.Error())
		return
	}
	items := make([]usageLogView, 0, len(logs))
	for _, l := range logs {
		items = append(items, logToView(l))
	}
	writeOK(w, map[string]any{
		"total": total, "limit": limit, "offset": offset, "items": items,
	})
}

// handleUsageSummary 按 model+key 聚合 since 以来的用量。
func (h *Handler) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "since 参数无法解析（支持 RFC3339 / unix 秒 / 24h 这类时长）")
		return
	}
	rows, err := h.st.UsageSummary(since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	writeOK(w, map[string]any{"items": rows})
}
