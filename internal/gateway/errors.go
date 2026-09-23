// Package gateway 对外暴露 Anthropic/OpenAI 兼容端点：sk- key 鉴权、
// 并发与限流、请求规范化、选号转发、SSE 透传与用量计费。
package gateway

import (
	"encoding/json"
	"net/http"
)

// endpoint 标识三个模型端点。
const (
	epMessages   = "messages"
	epResponses  = "responses"
	epChat       = "chat/completions"
	familyClaude = "claude"
	familyGPT    = "gpt"
	familyThird  = "third"
)

// writeError 按端点家族风格写错误体：/v1/messages 用 Anthropic 风格，
// 其余用 OpenAI 风格。
func writeError(w http.ResponseWriter, endpoint string, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if endpoint == epMessages {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": errType, "message": message},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"type": errType, "message": message},
	})
}

func writeAuthError(w http.ResponseWriter, endpoint, message string) {
	errType := "invalid_request_error"
	if endpoint == epMessages {
		errType = "authentication_error"
	}
	writeError(w, endpoint, http.StatusUnauthorized, errType, message)
}
