package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	// claudeFingerprint 是 relay 对 claude-* 模型要求的 Claude Code 身份行。
	claudeFingerprint = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	// systemByteLimit 是带指纹时 system 序列化的字节上限（见 PROTOCOL.md）。
	systemByteLimit = 200
	// modelPrefix 是客户端可能携带的路由前缀，转发前剥离。
	modelPrefix = "mirasim/"
)

// normResult 是请求规范化的产物。
type normResult struct {
	Body           []byte
	Model          string
	Stream         bool
	PromptCacheKey string
	AffinityKey    string
	Actions        []string
}

// normalize 按 PROTOCOL.md 改写请求体。endpoint 为 epMessages/epResponses/epChat 之一。
func normalize(endpoint string, raw []byte, cloakMode string) (*normResult, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errors.New("invalid JSON body")
	}
	res := &normResult{}
	model, _ := m["model"].(string)
	if model == "" {
		return nil, errors.New("model is required")
	}
	if stripped, ok := strings.CutPrefix(model, modelPrefix); ok {
		model = stripped
		m["model"] = model
		res.Actions = append(res.Actions, "strip_prefix")
	}
	res.Model = model
	res.Stream, _ = m["stream"].(bool)
	res.PromptCacheKey, _ = m["prompt_cache_key"].(string)

	switch endpoint {
	case epMessages:
		normalizeMessages(m, model, cloakMode, res)
	case epResponses:
		if input, ok := m["input"]; ok {
			if s, isStr := input.(string); isStr {
				m["input"] = []any{map[string]any{
					"role": "user",
					"content": []any{map[string]any{
						"type": "input_text",
						"text": s,
					}},
				}}
				res.Actions = append(res.Actions, "wrap_input")
			}
		}
	case epChat:
		// 不改写（relay 接受 top_p）
	}

	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	res.Body = body
	res.AffinityKey = affinityKey(res, m)
	return res, nil
}

// normalizeMessages 处理 /v1/messages：system 提顶层、剔 top_p、
// claude-* 注入指纹并按 cloakMode 做 200 字节截断。
func normalizeMessages(m map[string]any, model, cloakMode string, res *normResult) {
	if msgs, ok := m["messages"].([]any); ok {
		var kept, hoisted []any
		for _, item := range msgs {
			msg, ok := item.(map[string]any)
			if !ok || msg["role"] != "system" {
				kept = append(kept, item)
				continue
			}
			hoisted = append(hoisted, contentBlocks(msg["content"])...)
		}
		if len(hoisted) > 0 {
			blocks := append(contentBlocks(m["system"]), hoisted...)
			m["messages"] = kept
			m["system"] = blocks
			res.Actions = append(res.Actions, "hoist_system")
		}
	}
	if _, ok := m["top_p"]; ok {
		delete(m, "top_p")
		res.Actions = append(res.Actions, "strip_top_p")
	}

	if !strings.HasPrefix(model, familyClaude) {
		return // 第三方模型不注入不截断
	}
	if !systemHasFingerprint(m["system"]) {
		blocks := append([]any{textBlock(claudeFingerprint)}, contentBlocks(m["system"])...)
		m["system"] = blocks
		res.Actions = append(res.Actions, "inject_fingerprint")
	}
	if size := systemLen(m["system"]); size <= systemByteLimit {
		return
	}
	if cloakMode == "strict" {
		m["system"] = []any{textBlock(claudeFingerprint)}
		res.Actions = append(res.Actions, "cloak_strict")
		return
	}
	m["system"] = truncateSystemBlocks(contentBlocks(m["system"]), systemByteLimit)
	res.Actions = append(res.Actions, "cloak_truncate")
}

// contentBlocks 把 string/数组形态的内容统一为 text block 数组。
func contentBlocks(v any) []any {
	switch c := v.(type) {
	case nil:
		return nil
	case string:
		return []any{textBlock(c)}
	case []any:
		out := make([]any, 0, len(c))
		for _, item := range c {
			if s, ok := item.(string); ok {
				out = append(out, textBlock(s))
				continue
			}
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func textBlock(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

// blockText 提取 text block 的文本；非文本块返回空。
func blockText(v any) string {
	if m, ok := v.(map[string]any); ok {
		s, _ := m["text"].(string)
		return s
	}
	return ""
}

func systemHasFingerprint(system any) bool {
	switch s := system.(type) {
	case string:
		return strings.Contains(s, claudeFingerprint)
	case []any:
		for _, item := range s {
			if strings.Contains(blockText(item), claudeFingerprint) {
				return true
			}
		}
	}
	return false
}

// systemLen 计算 system 的序列化字节数（string 原文 / block 文本总和）。
func systemLen(system any) int {
	switch s := system.(type) {
	case string:
		return len(s)
	case []any:
		n := 0
		for _, item := range s {
			n += len(blockText(item))
		}
		return n
	}
	return 0
}

// truncateSystemBlocks 按字节截断 block 文本（保头丢尾；指纹行在头部，
// 天然保留完整）。截断点回退到完整 UTF-8 字符边界。
func truncateSystemBlocks(blocks []any, limit int) []any {
	var out []any
	remaining := limit
	for _, b := range blocks {
		text := blockText(b)
		if len(text) > remaining {
			text = text[:remaining]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		if text != "" {
			out = append(out, textBlock(text))
			remaining -= len(text)
		}
		if remaining <= 0 {
			break
		}
	}
	return out
}

// affinityKey 计算会话粘性键：优先 prompt_cache_key，否则
// sha256hex(model + system 首部 + 首条消息首部) 截 16 字符。
func affinityKey(res *normResult, raw map[string]any) string {
	if res.PromptCacheKey != "" {
		return res.PromptCacheKey
	}
	h := sha256.New()
	h.Write([]byte(res.Model))
	writePrefix := func(v any) {
		if v == nil {
			return
		}
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		if len(b) > 128 {
			b = b[:128]
		}
		h.Write(b)
	}
	writePrefix(raw["system"])
	if msgs, ok := raw["messages"].([]any); ok && len(msgs) > 0 {
		writePrefix(msgs[0])
	}
	if input, ok := raw["input"]; ok {
		writePrefix(input)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// modelFamily 判定模型家族：claude-* / gpt-* / 第三方。
func modelFamily(model string) string {
	switch {
	case strings.HasPrefix(model, familyClaude):
		return familyClaude
	case strings.HasPrefix(model, familyGPT):
		return familyGPT
	default:
		return familyThird
	}
}
