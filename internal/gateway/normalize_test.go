package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func unmarshal(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func systemTexts(t *testing.T, system any) []string {
	t.Helper()
	var out []string
	switch s := system.(type) {
	case string:
		out = append(out, s)
	case []any:
		for _, item := range s {
			out = append(out, blockText(item))
		}
	default:
		t.Fatalf("system 形态异常: %T", system)
	}
	return out
}

func TestNormalizeMessagesHoistAndStrip(t *testing.T) {
	raw := `{"model":"mirasim/kimi-k3","top_p":0.5,"messages":[
		{"role":"system","content":"S1"},
		{"role":"user","content":"hi"},
		{"role":"system","content":[{"type":"text","text":"S2"}]}
	]}`
	res, err := normalize(epMessages, []byte(raw), "relaxed")
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "kimi-k3" {
		t.Fatalf("前缀未剥离: %s", res.Model)
	}
	m := unmarshal(t, res.Body)
	if _, ok := m["top_p"]; ok {
		t.Fatal("top_p 应被剔除")
	}
	if msgs := m["messages"].([]any); len(msgs) != 1 {
		t.Fatalf("system 消息应被提到顶层: %d 条剩余", len(msgs))
	}
	texts := systemTexts(t, m["system"])
	if len(texts) != 2 || texts[0] != "S1" || texts[1] != "S2" {
		t.Fatalf("system 合并顺序不符: %v", texts)
	}
	for _, want := range []string{"strip_prefix", "hoist_system", "strip_top_p"} {
		found := false
		for _, a := range res.Actions {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("缺少规范化动作 %s: %v", want, res.Actions)
		}
	}
	// 第三方模型不应注入指纹
	for _, s := range systemTexts(t, m["system"]) {
		if strings.Contains(s, claudeFingerprint) {
			t.Fatal("第三方模型不应注入指纹")
		}
	}
}

func TestNormalizeMessagesHoistMergesTopLevelSystem(t *testing.T) {
	raw := `{"model":"kimi-k3","system":"TOP","messages":[
		{"role":"system","content":"S1"},{"role":"user","content":"hi"}]}`
	res, err := normalize(epMessages, []byte(raw), "relaxed")
	if err != nil {
		t.Fatal(err)
	}
	texts := systemTexts(t, unmarshal(t, res.Body)["system"])
	if len(texts) != 2 || texts[0] != "TOP" || texts[1] != "S1" {
		t.Fatalf("顶层 system 应在前: %v", texts)
	}
}

func TestNormalizeFingerprintClaudeOnly(t *testing.T) {
	// claude 无 system → 注入指纹
	res, err := normalize(epMessages, []byte(
		`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`), "relaxed")
	if err != nil {
		t.Fatal(err)
	}
	texts := systemTexts(t, unmarshal(t, res.Body)["system"])
	if len(texts) != 1 || texts[0] != claudeFingerprint {
		t.Fatalf("应注入指纹行: %v", texts)
	}
	// 已含指纹 → 不重复注入
	raw := `{"model":"claude-sonnet-5","system":"` + claudeFingerprint + `","messages":[]}`
	res, err = normalize(epMessages, []byte(raw), "relaxed")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range res.Actions {
		if a == "inject_fingerprint" {
			t.Fatal("已有指纹不应重复注入")
		}
	}
}

func TestNormalizeCloakRelaxed(t *testing.T) {
	long := strings.Repeat("a", 300)
	raw := `{"model":"claude-opus-5","system":"` + long + `","messages":[]}`
	res, err := normalize(epMessages, []byte(raw), "relaxed")
	if err != nil {
		t.Fatal(err)
	}
	m := unmarshal(t, res.Body)
	texts := systemTexts(t, m["system"])
	if texts[0] != claudeFingerprint {
		t.Fatalf("指纹行应完整保留在头部: %q", texts[0])
	}
	total := 0
	for _, s := range texts {
		total += len(s)
	}
	if total > systemByteLimit {
		t.Fatalf("relaxed 截断后应 ≤200 字节: %d", total)
	}
	if total != systemByteLimit {
		t.Fatalf("应截满 200 字节: %d", total)
	}
}

func TestNormalizeCloakStrict(t *testing.T) {
	long := strings.Repeat("a", 300)
	raw := `{"model":"claude-opus-5","system":"` + long + `","messages":[]}`
	res, err := normalize(epMessages, []byte(raw), "strict")
	if err != nil {
		t.Fatal(err)
	}
	texts := systemTexts(t, unmarshal(t, res.Body)["system"])
	if len(texts) != 1 || texts[0] != claudeFingerprint {
		t.Fatalf("strict 应只发指纹行: %v", texts)
	}
}

func TestNormalizeThirdPartyNoTruncate(t *testing.T) {
	long := strings.Repeat("a", 500)
	raw := `{"model":"deepseek-flash","system":"` + long + `","messages":[]}`
	res, err := normalize(epMessages, []byte(raw), "strict")
	if err != nil {
		t.Fatal(err)
	}
	texts := systemTexts(t, unmarshal(t, res.Body)["system"])
	if len(texts) != 1 || texts[0] != long {
		t.Fatal("第三方模型不应注入也不应截断")
	}
}

func TestNormalizeResponsesWrapsStringInput(t *testing.T) {
	res, err := normalize(epResponses, []byte(
		`{"model":"gpt-6-astra","input":"hello"}`), "relaxed")
	if err != nil {
		t.Fatal(err)
	}
	m := unmarshal(t, res.Body)
	input, ok := m["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("字符串 input 应包装为数组: %v", m["input"])
	}
	msg := input[0].(map[string]any)
	if msg["role"] != "user" {
		t.Fatalf("role 应为 user: %v", msg)
	}
	content := msg["content"].([]any)[0].(map[string]any)
	if content["type"] != "input_text" || content["text"] != "hello" {
		t.Fatalf("content 包装不符: %v", content)
	}
	// 数组 input 不动
	res, err = normalize(epResponses, []byte(
		`{"model":"gpt-6-astra","input":[{"role":"user","content":[]}]}`), "relaxed")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range res.Actions {
		if a == "wrap_input" {
			t.Fatal("数组 input 不应包装")
		}
	}
}

func TestNormalizeChatKeepsTopP(t *testing.T) {
	res, err := normalize(epChat, []byte(
		`{"model":"kimi-k3","top_p":0.9,"messages":[]}`), "relaxed")
	if err != nil {
		t.Fatal(err)
	}
	m := unmarshal(t, res.Body)
	if _, ok := m["top_p"]; !ok {
		t.Fatal("chat/completions 不应剔除 top_p")
	}
}

func TestAffinityKey(t *testing.T) {
	withKey, err := normalize(epMessages, []byte(
		`{"model":"kimi-k3","prompt_cache_key":"sess-1","messages":[{"role":"user","content":"hi"}]}`), "relaxed")
	if err != nil {
		t.Fatal(err)
	}
	if withKey.AffinityKey != "sess-1" {
		t.Fatalf("应优先用 prompt_cache_key: %s", withKey.AffinityKey)
	}
	mk := func(content string) string {
		res, err := normalize(epMessages, []byte(
			`{"model":"kimi-k3","messages":[{"role":"user","content":"`+content+`"}]}`), "relaxed")
		if err != nil {
			t.Fatal(err)
		}
		return res.AffinityKey
	}
	if mk("a") != mk("a") || mk("a") == mk("b") {
		t.Fatal("sha256 粘性键不稳定")
	}
	if len(mk("a")) != 16 {
		t.Fatalf("粘性键应截 16 字符: %s", mk("a"))
	}
}

func TestNormalizeInvalidBody(t *testing.T) {
	if _, err := normalize(epMessages, []byte(`{bad`), "relaxed"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
	if _, err := normalize(epMessages, []byte(`{"messages":[]}`), "relaxed"); err == nil {
		t.Fatal("缺 model 应报错")
	}
}
