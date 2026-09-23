package admin

import (
	"net/http"
	"sort"

	"mirasim2api/internal/runtimecfg"
)

// 可热更键的顺序（管理端按此展示）。
var settingKeys = []string{
	runtimecfg.KeyUpstreamProxy,
	runtimecfg.KeyCloakMode,
	runtimecfg.KeyCapacityRetries,
	runtimecfg.KeyCapacityBackoffMS,
	runtimecfg.KeyModelPrices,
	runtimecfg.KeyRateMultiplier,
}

// settingItem 是单项设置的描述。
type settingItem struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Source  string `json:"source"` // database / env / default
	Default string `json:"default"`
}

// handleSettingsGet 返回所有可热更设置项。
func (h *Handler) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	items := make([]settingItem, 0, len(settingKeys))
	for _, k := range settingKeys {
		items = append(items, settingItem{
			Key:     k,
			Value:   h.settings.Get(k),
			Source:  h.settings.Source(k),
			Default: h.settings.Default(k),
		})
	}
	writeOK(w, map[string]any{"settings": items})
}

// handleSettingsPut 批量写入设置项。仅接受白名单键；空字符串表示删除（回落到 env/默认）。
func (h *Handler) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	allowed := map[string]bool{}
	for _, k := range settingKeys {
		allowed[k] = true
	}
	keys := make([]string, 0, len(body))
	for k := range body {
		if !allowed[k] {
			writeErr(w, http.StatusBadRequest, "不支持的设置键: "+k)
			return
		}
		keys = append(keys, k)
	}
	// 固定顺序写库，保证测试与审计可预期。
	sort.Strings(keys)
	for _, k := range keys {
		if err := h.st.SetSetting(k, body[k]); err != nil {
			writeErr(w, http.StatusInternalServerError, "写入设置失败: "+err.Error())
			return
		}
	}
	h.handleSettingsGet(w, r)
}
