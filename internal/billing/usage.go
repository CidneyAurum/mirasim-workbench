// Package billing 负责用量解析与费用记录：从响应（含 SSE 流）提取
// token 用量，按模型价格表 × 倍率计费，批量落库。
package billing

import (
	"bytes"
	"encoding/json"
)

// Usage 是一次请求的 token 用量。
type Usage struct {
	Input  int64
	Output int64
	Cached int64
}

// sseDataPayloads 把 SSE 字节流拆成各事件的 data 载荷（多行 data 以 \n 拼接）。
// 输入不是 SSE（无 data: 行）时返回 nil。
func sseDataPayloads(data []byte) [][]byte {
	var payloads [][]byte
	for _, block := range bytes.Split(data, []byte("\n\n")) {
		var payload []byte
		for _, line := range bytes.Split(block, []byte("\n")) {
			line = bytes.TrimRight(line, "\r")
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			v := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if payload != nil {
				payload = append(payload, '\n')
			}
			payload = append(payload, v...)
		}
		if payload != nil && !bytes.Equal(payload, []byte("[DONE]")) {
			payloads = append(payloads, payload)
		}
	}
	return payloads
}

type anthropicUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// ParseAnthropicUsage 从 /v1/messages 响应提取用量：SSE（message_start 的
// message.usage + message_delta 的 usage）或完整 JSON（顶层 usage）。
// input = input_tokens + cache_creation_input_tokens；cached = cache_read_input_tokens；
// output 取各帧最大值（message_delta 为累计值）。
func ParseAnthropicUsage(data []byte) Usage {
	payloads := sseDataPayloads(data)
	if len(payloads) == 0 {
		payloads = [][]byte{data}
	}
	var u Usage
	for _, p := range payloads {
		var obj struct {
			Type    string          `json:"type"`
			Usage   *anthropicUsage `json:"usage"`
			Message *struct {
				Usage *anthropicUsage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(p, &obj) != nil {
			continue
		}
		au := obj.Usage
		if obj.Message != nil && obj.Message.Usage != nil {
			au = obj.Message.Usage
		}
		if au == nil {
			continue
		}
		if in := au.InputTokens + au.CacheCreationInputTokens; in > u.Input {
			u.Input = in
		}
		if au.CacheReadInputTokens > u.Cached {
			u.Cached = au.CacheReadInputTokens
		}
		if au.OutputTokens > u.Output {
			u.Output = au.OutputTokens
		}
	}
	return u
}

type responsesUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// ParseResponsesUsage 从 /v1/responses 响应提取用量：SSE 末尾
// response.completed 事件的 response.usage，或完整 JSON 的顶层 usage。
func ParseResponsesUsage(data []byte) Usage {
	payloads := sseDataPayloads(data)
	if len(payloads) == 0 {
		payloads = [][]byte{data}
	}
	var u Usage
	for _, p := range payloads {
		var obj struct {
			Type     string          `json:"type"`
			Usage    *responsesUsage `json:"usage"`
			Response *struct {
				Usage *responsesUsage `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal(p, &obj) != nil {
			continue
		}
		ru := obj.Usage
		if obj.Response != nil && obj.Response.Usage != nil {
			ru = obj.Response.Usage
		}
		if ru == nil {
			continue
		}
		if ru.InputTokens > u.Input {
			u.Input = ru.InputTokens
		}
		if ru.OutputTokens > u.Output {
			u.Output = ru.OutputTokens
		}
		if ru.InputTokensDetails.CachedTokens > u.Cached {
			u.Cached = ru.InputTokensDetails.CachedTokens
		}
	}
	return u
}

type chatUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// ParseChatUsage 从 /v1/chat/completions 响应提取用量：SSE 末帧的 usage
// 或完整 JSON 的顶层 usage（prompt_tokens/completion_tokens）。
func ParseChatUsage(data []byte) Usage {
	payloads := sseDataPayloads(data)
	if len(payloads) == 0 {
		payloads = [][]byte{data}
	}
	var u Usage
	for _, p := range payloads {
		var obj struct {
			Usage *chatUsage `json:"usage"`
		}
		if json.Unmarshal(p, &obj) != nil || obj.Usage == nil {
			continue
		}
		if obj.Usage.PromptTokens > u.Input {
			u.Input = obj.Usage.PromptTokens
		}
		if obj.Usage.CompletionTokens > u.Output {
			u.Output = obj.Usage.CompletionTokens
		}
		if obj.Usage.PromptTokensDetails.CachedTokens > u.Cached {
			u.Cached = obj.Usage.PromptTokensDetails.CachedTokens
		}
	}
	return u
}
