package billing

import (
	"math"
	"testing"
	"time"

	"mirasim2api/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestParseAnthropicUsageSSE(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":25,"output_tokens":1,"cache_creation_input_tokens":5,"cache_read_input_tokens":10}}}

event: content_block_delta
data: {"type":"content_block_delta","delta":{"text":"hi"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}

`
	u := ParseAnthropicUsage([]byte(sse))
	if u.Input != 30 || u.Output != 42 || u.Cached != 10 {
		t.Fatalf("anthropic SSE usage 不符: %+v", u)
	}
}

func TestParseAnthropicUsageJSON(t *testing.T) {
	body := `{"id":"msg_1","type":"message","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":7}}`
	u := ParseAnthropicUsage([]byte(body))
	if u.Input != 100 || u.Output != 20 || u.Cached != 7 {
		t.Fatalf("anthropic JSON usage 不符: %+v", u)
	}
}

func TestParseResponsesUsage(t *testing.T) {
	sse := `event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"hi"}

event: response.completed
data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":33,"output_tokens":11,"input_tokens_details":{"cached_tokens":4}}}}

`
	u := ParseResponsesUsage([]byte(sse))
	if u.Input != 33 || u.Output != 11 || u.Cached != 4 {
		t.Fatalf("responses SSE usage 不符: %+v", u)
	}
	body := `{"id":"r2","usage":{"input_tokens":5,"output_tokens":6}}`
	u = ParseResponsesUsage([]byte(body))
	if u.Input != 5 || u.Output != 6 || u.Cached != 0 {
		t.Fatalf("responses JSON usage 不符: %+v", u)
	}
}

func TestParseChatUsage(t *testing.T) {
	sse := `data: {"id":"c1","choices":[{"delta":{"content":"hi"}}]}

data: {"id":"c1","choices":[],"usage":{"prompt_tokens":15,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":3}}}

data: [DONE]

`
	u := ParseChatUsage([]byte(sse))
	if u.Input != 15 || u.Output != 8 || u.Cached != 3 {
		t.Fatalf("chat SSE usage 不符: %+v", u)
	}
	body := `{"usage":{"prompt_tokens":2,"completion_tokens":3}}`
	u = ParseChatUsage([]byte(body))
	if u.Input != 2 || u.Output != 3 {
		t.Fatalf("chat JSON usage 不符: %+v", u)
	}
}

func TestRecorderCostAndFlush(t *testing.T) {
	st := newTestStore(t)
	if err := st.SetSetting("model_prices", `{"claude-sonnet-5":{"input":3,"output":15,"cached":0.3}}`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("rate_multiplier", "2"); err != nil {
		t.Fatal(err)
	}
	rec := NewRecorder(st)
	defer rec.Close()
	plaintext, key, err := st.CreateAPIKey("k1", store.APIKeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	_ = plaintext

	// (1000*3 + 500*15 + 2000*0.3)/1e6 * 2 = (3000+7500+600)/1e6*2 = 0.0222
	rec.Record(&store.UsageLog{
		APIKeyID: key.ID, AccountID: "acc1", Model: "claude-sonnet-5", Endpoint: "/v1/messages",
		InputTokens: 1000, OutputTokens: 500, CachedTokens: 2000, Status: 200,
	})
	// usage 为 0 的失败请求也落日志，cost=0
	rec.Record(&store.UsageLog{
		APIKeyID: key.ID, Model: "claude-sonnet-5", Endpoint: "/v1/messages",
		Status: 503, Err: "model_capacity_exhausted",
	})
	rec.Flush()

	logs, err := st.QueryUsageLogs(store.UsageLogFilter{KeyID: key.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 {
		t.Fatalf("应有 2 条日志: %d", len(logs))
	}
	var okLog, failLog *store.UsageLog
	for _, l := range logs {
		if l.Status == 200 {
			okLog = l
		} else {
			failLog = l
		}
	}
	if okLog == nil || failLog == nil {
		t.Fatalf("日志状态不符: %+v", logs)
	}
	if math.Abs(okLog.Cost-0.0222) > 1e-9 {
		t.Fatalf("cost 不符: %f", okLog.Cost)
	}
	if failLog.Cost != 0 || failLog.Err == "" {
		t.Fatalf("失败日志应 cost=0 且带 err: %+v", failLog)
	}
	// key 冗余累计
	got, err := st.GetAPIKeyByPlaintext(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalInputTokens != 1000 || got.TotalOutputTokens != 500 || math.Abs(got.TotalCost-0.0222) > 1e-9 {
		t.Fatalf("key 累计不符: %+v", got)
	}
}

func TestRecorderDefaultPricesAndPrefixMatch(t *testing.T) {
	st := newTestStore(t)
	rec := NewRecorder(st)
	defer rec.Close()
	// 默认表：claude-opus-5 15/75/1.5；带日期后缀应前缀匹配
	c := rec.Cost("claude-opus-5-20260901", 1_000_000, 0, 0)
	if math.Abs(c-15) > 1e-9 {
		t.Fatalf("默认价格/前缀匹配不符: %f", c)
	}
	// 未定价模型为 0
	if c := rec.Cost("kimi-k3", 1_000_000, 1_000_000, 0); c != 0 {
		t.Fatalf("未定价模型应 0: %f", c)
	}
}

func TestRecorderBatchFlush(t *testing.T) {
	st := newTestStore(t)
	rec := NewRecorder(st)
	defer rec.Close()
	for range 25 { // 超过一批 20 条，应触发自动 flush
		rec.Record(&store.UsageLog{Model: "kimi-k3", Status: 200})
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		logs, err := st.QueryUsageLogs(store.UsageLogFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(logs) >= 20 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("满 20 条应自动 flush")
}
