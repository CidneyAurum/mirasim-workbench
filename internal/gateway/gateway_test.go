package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mirasim2api/internal/billing"
	"mirasim2api/internal/config"
	"mirasim2api/internal/mirasim"
	"mirasim2api/internal/pool"
	"mirasim2api/internal/store"
)

// mockFetcher 实现 pool.LimitFetcher。
type mockFetcher struct{}

func (mockFetcher) FetchLimits(context.Context) (*pool.Limits, error) {
	return &pool.Limits{FetchedAt: time.Now()}, nil
}

// fakeRelay 处理凭据链端点（/auth/refresh、/v1/device/session），
// 模型端点委派给可替换的 fn。ticket 按 deviceId 区分账号。
type fakeRelay struct {
	srv          *httptest.Server
	mu           sync.Mutex
	fn           http.HandlerFunc
	sessionCalls atomic.Int64
}

func newFakeRelay(t *testing.T, fn http.HandlerFunc) *fakeRelay {
	t.Helper()
	f := &fakeRelay{fn: fn}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRelay) setFn(fn http.HandlerFunc) {
	f.mu.Lock()
	f.fn = fn
	f.mu.Unlock()
}

func (f *fakeRelay) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/auth/refresh":
		fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","expires_in":3600}`)
	case "/v1/device/session":
		f.sessionCalls.Add(1)
		var body struct {
			DeviceID string `json:"deviceId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprintf(w, `{"ticket":"tk-%s","expires_in":900}`, body.DeviceID)
	default:
		f.mu.Lock()
		fn := f.fn
		f.mu.Unlock()
		fn(w, r)
	}
}

type fixture struct {
	st      *store.Store
	pool    *pool.Pool
	rec     *billing.Recorder
	cfg     *config.Config
	relay   *fakeRelay
	gw      *httptest.Server
	keySlot time.Duration
}

func newFixture(t *testing.T, relayFn http.HandlerFunc, mutate func(*fixture)) *fixture {
	t.Helper()
	fx := &fixture{relay: newFakeRelay(t, relayFn)}
	var err error
	fx.st, err = store.Open(t.TempDir(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fx.st.Close() })
	fx.pool = pool.New(fx.st, func(acc *store.Account) (*mirasim.Client, error) {
		priv, _ := mirasim.GenerateDeviceKey()
		return mirasim.NewClient(acc.RefreshToken, priv, fx.relay.srv.URL, fx.relay.srv.URL, "", "", "")
	})
	fx.rec = billing.NewRecorder(fx.st)
	t.Cleanup(fx.rec.Close)
	fx.cfg = &config.Config{
		GatewayKeyRequired:    true,
		ClaudeCloakMode:       "relaxed",
		CapacityRetries:       2,
		CapacityBackoffMS:     1,
		AccountMaxConcurrency: 5,
	}
	if mutate != nil {
		mutate(fx)
	}
	fx.gw = httptest.NewServer(NewHandler(Deps{
		Store: fx.st, Pool: fx.pool, Config: fx.cfg, Billing: fx.rec,
		KeySlotTimeout: fx.keySlot,
	}))
	t.Cleanup(fx.gw.Close)
	return fx
}

func (fx *fixture) addAccount(t *testing.T, id string) *pool.Entry {
	t.Helper()
	priv, err := mirasim.GenerateDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	client, err := mirasim.NewClient("rt-"+id, priv, fx.relay.srv.URL, fx.relay.srv.URL, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	e := &pool.Entry{
		Account: &store.Account{ID: id, Email: id + "@x.com", Enabled: true},
		Client:  client,
		Fetcher: mockFetcher{},
	}
	fx.pool.Add(e)
	return e
}

func (fx *fixture) createKey(t *testing.T, name string, opts store.APIKeyOpts) string {
	t.Helper()
	plaintext, _, err := fx.st.CreateAPIKey(name, opts)
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func okRelay(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"id":"msg_1","type":"message","usage":{"input_tokens":10,"output_tokens":5}}`)
}

// post 发模型请求并读完整响应。
func post(t *testing.T, url, key, body string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, b
}

const chatBody = `{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`

// ---------- 鉴权 ----------

func TestAuthFailures(t *testing.T) {
	fx := newFixture(t, okRelay, func(fx *fixture) { fx.addAccount(t, "a1") })

	t.Run("无 key", func(t *testing.T) {
		status, _, body := post(t, fx.gw.URL+"/v1/messages", "",
			`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`)
		if status != 401 {
			t.Fatalf("应 401: %d", status)
		}
		// Anthropic 风格错误体
		var m map[string]any
		if json.Unmarshal(body, &m) != nil || m["type"] != "error" {
			t.Fatalf("/v1/messages 应为 Anthropic 错误体: %s", body)
		}
		if m["error"].(map[string]any)["type"] != "authentication_error" {
			t.Fatalf("错误类型不符: %s", body)
		}
	})
	t.Run("错误 key", func(t *testing.T) {
		status, _, body := post(t, fx.gw.URL+"/v1/chat/completions", "sk-nope", chatBody)
		if status != 401 {
			t.Fatalf("应 401: %d", status)
		}
		var m map[string]any
		if json.Unmarshal(body, &m) != nil || m["error"].(map[string]any)["type"] != "invalid_request_error" {
			t.Fatalf("/v1/chat/completions 应为 OpenAI 错误体: %s", body)
		}
	})
	t.Run("x-api-key 头可用", func(t *testing.T) {
		key := fx.createKey(t, "k", store.APIKeyOpts{})
		req, _ := http.NewRequest(http.MethodPost, fx.gw.URL+"/v1/chat/completions", strings.NewReader(chatBody))
		req.Header.Set("x-api-key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("x-api-key 应可用: %d", resp.StatusCode)
		}
	})
	t.Run("禁用 key", func(t *testing.T) {
		key := fx.createKey(t, "dis", store.APIKeyOpts{})
		k, _ := fx.st.GetAPIKeyByPlaintext(key)
		enabled := false
		if err := fx.st.UpdateAPIKey(k.ID, store.APIKeyUpdate{Enabled: &enabled}); err != nil {
			t.Fatal(err)
		}
		status, _, _ := post(t, fx.gw.URL+"/v1/chat/completions", key, chatBody)
		if status != 401 {
			t.Fatalf("禁用 key 应 401: %d", status)
		}
	})
	t.Run("过期 key", func(t *testing.T) {
		past := time.Now().Add(-time.Hour)
		key := fx.createKey(t, "exp", store.APIKeyOpts{ExpiresAt: &past})
		status, _, _ := post(t, fx.gw.URL+"/v1/chat/completions", key, chatBody)
		if status != 401 {
			t.Fatalf("过期 key 应 401: %d", status)
		}
	})
}

func TestModelAllowlist(t *testing.T) {
	fx := newFixture(t, okRelay, func(fx *fixture) { fx.addAccount(t, "a1") })
	key := fx.createKey(t, "wl", store.APIKeyOpts{ModelAllowlist: []string{"kimi-k3"}})
	status, _, body := post(t, fx.gw.URL+"/v1/chat/completions", key,
		`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`)
	if status != 403 {
		t.Fatalf("白名单外模型应 403: %d %s", status, body)
	}
	status, _, _ = post(t, fx.gw.URL+"/v1/chat/completions", key, chatBody)
	if status != 200 {
		t.Fatalf("白名单内模型应放行: %d", status)
	}
}

func TestFamilyRouting(t *testing.T) {
	fx := newFixture(t, okRelay, func(fx *fixture) { fx.addAccount(t, "a1") })
	key := fx.createKey(t, "k", store.APIKeyOpts{})

	status, _, body := post(t, fx.gw.URL+"/v1/chat/completions", key,
		`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`)
	if status != 400 || !strings.Contains(string(body), "/v1/messages") {
		t.Fatalf("claude 发到 chat/completions 应 400 并提示 /v1/messages: %d %s", status, body)
	}
	status, _, _ = post(t, fx.gw.URL+"/v1/messages", key,
		`{"model":"gpt-6-astra","messages":[{"role":"user","content":"hi"}]}`)
	if status != 400 {
		t.Fatalf("gpt 发到 messages 应 400: %d", status)
	}
	// 第三方三个端点都行
	for _, ep := range []string{"/v1/messages", "/v1/responses", "/v1/chat/completions"} {
		b := `{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"input":[{"role":"user","content":[]}]}`
		status, _, _ := post(t, fx.gw.URL+ep, key, b)
		if status != 200 {
			t.Fatalf("kimi-k3 在 %s 应可用: %d", ep, status)
		}
	}
}

// ---------- 并发与限流 ----------

func TestKeyConcurrency429(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	fx := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		okRelay(w, nil)
	}, func(fx *fixture) {
		fx.addAccount(t, "a1")
		fx.keySlot = 300 * time.Millisecond
	})
	key := fx.createKey(t, "c1", store.APIKeyOpts{Concurrency: 1})

	done := make(chan int, 1)
	go func() {
		status, _, _ := post(t, fx.gw.URL+"/v1/chat/completions", key, chatBody)
		done <- status
	}()
	<-entered
	status, _, _ := post(t, fx.gw.URL+"/v1/chat/completions", key, chatBody)
	if status != 429 {
		t.Fatalf("并发槽占满应 429: %d", status)
	}
	close(release) // 放行第一个请求
	if s := <-done; s != 200 {
		t.Fatalf("第一个请求应成功: %d", s)
	}
}

func TestRateLimit429(t *testing.T) {
	fx := newFixture(t, okRelay, func(fx *fixture) { fx.addAccount(t, "a1") })
	key := fx.createKey(t, "rpm", store.APIKeyOpts{RateLimitRPM: 2})
	for i := range 3 {
		status, hdr, _ := post(t, fx.gw.URL+"/v1/chat/completions", key, chatBody)
		if i < 2 {
			if status != 200 {
				t.Fatalf("第 %d 次应成功: %d", i+1, status)
			}
			continue
		}
		if status != 429 {
			t.Fatalf("超出 RPM 应 429: %d", status)
		}
		if hdr.Get("Retry-After") == "" {
			t.Fatal("429 应带 Retry-After")
		}
	}
}

// ---------- 转发与重试 ----------

func TestCapacityRetry(t *testing.T) {
	var mu sync.Mutex
	var models []string
	calls := 0
	fx := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		models = append(models, fmt.Sprint(m["model"]))
		calls++
		n := calls
		mu.Unlock()
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"type":"model_capacity_exhausted"}}`)
			return
		}
		okRelay(w, r)
	}, func(fx *fixture) { fx.addAccount(t, "a1") })
	key := fx.createKey(t, "k", store.APIKeyOpts{})

	status, hdr, _ := post(t, fx.gw.URL+"/v1/chat/completions", key,
		`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`)
	if status != 200 {
		t.Fatalf("重试后应成功: %d", status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(models) != 3 {
		t.Fatalf("应请求 3 次: %d", len(models))
	}
	for _, m := range models {
		if m != "kimi-k3" {
			t.Fatalf("三次模型名应不变: %v", models)
		}
	}
	if hdr.Get(capacityRetriesHeader) != "2" {
		t.Fatalf("响应头应带重试次数 2: %v", hdr)
	}
	// 503 容量耗尽不熔断账号
	for _, st := range fx.pool.Stats() {
		if !st.CooldownUntil.IsZero() || st.Failures != 0 {
			t.Fatalf("容量 503 不应熔断账号: %+v", st)
		}
	}
}

func TestCapacityRetryExhausted(t *testing.T) {
	fx := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"type":"model_capacity_exhausted"}}`)
	}, func(fx *fixture) { fx.addAccount(t, "a1") })
	key := fx.createKey(t, "k", store.APIKeyOpts{})
	status, _, body := post(t, fx.gw.URL+"/v1/chat/completions", key, chatBody)
	if status != 503 || !strings.Contains(string(body), "model_capacity_exhausted") {
		t.Fatalf("重试用尽应透传 503: %d %s", status, body)
	}
}

func TestUnauthorizedSwitchAccount(t *testing.T) {
	var modelCalls atomic.Int64
	var tickets sync.Map // 出现过的 Authorization ticket
	fx := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		tickets.LoadOrStore(r.Header.Get("Authorization"), true)
		if modelCalls.Add(1) == 1 {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":"ticket expired"}`)
			return
		}
		okRelay(w, r)
	}, func(fx *fixture) {
		fx.addAccount(t, "a1")
		fx.addAccount(t, "a2")
	})
	key := fx.createKey(t, "k", store.APIKeyOpts{})

	status, _, body := post(t, fx.gw.URL+"/v1/chat/completions", key, chatBody)
	if status != 200 {
		t.Fatalf("换账号重试应成功: %d %s", status, body)
	}
	if modelCalls.Load() != 2 {
		t.Fatalf("应调用 2 次上游: %d", modelCalls.Load())
	}
	n := 0
	tickets.Range(func(_, _ any) bool { n++; return true })
	if n != 2 {
		t.Fatalf("应换用另一个账号（不同 ticket）: %d", n)
	}
	if fx.relay.sessionCalls.Load() < 2 {
		t.Fatal("两个账号都应申领过 ticket")
	}
}

func TestUpstream4xxPassthroughAndCooldown(t *testing.T) {
	fx := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(422)
		fmt.Fprint(w, `{"error":"model is not supported"}`)
	}, func(fx *fixture) { fx.addAccount(t, "a1") })
	key := fx.createKey(t, "k", store.APIKeyOpts{})
	status, _, body := post(t, fx.gw.URL+"/v1/chat/completions", key, chatBody)
	if status != 422 || !strings.Contains(string(body), "not supported") {
		t.Fatalf("4xx 应如实透传: %d %s", status, body)
	}
	for _, st := range fx.pool.Stats() {
		if st.CooldownUntil.IsZero() {
			t.Fatal("4xx 应触发账号退避冷却")
		}
	}
}

func TestNoAccounts503(t *testing.T) {
	fx := newFixture(t, okRelay, nil)
	key := fx.createKey(t, "k", store.APIKeyOpts{})
	status, _, body := post(t, fx.gw.URL+"/v1/chat/completions", key, chatBody)
	if status != 503 || !strings.Contains(string(body), "no available") {
		t.Fatalf("无可用账号应 503: %d %s", status, body)
	}
}

func TestAliasPaths(t *testing.T) {
	fx := newFixture(t, okRelay, func(fx *fixture) { fx.addAccount(t, "a1") })
	key := fx.createKey(t, "k", store.APIKeyOpts{})
	status, _, _ := post(t, fx.gw.URL+"/chat/completions", key, chatBody)
	if status != 200 {
		t.Fatalf("无 /v1 前缀别名应可用: %d", status)
	}
}

// ---------- 规范化落到上游 ----------

func TestUpstreamReceivesNormalizedBody(t *testing.T) {
	var got []byte
	fx := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		okRelay(w, r)
	}, func(fx *fixture) { fx.addAccount(t, "a1") })
	key := fx.createKey(t, "k", store.APIKeyOpts{})

	status, _, _ := post(t, fx.gw.URL+"/v1/messages", key,
		`{"model":"mirasim/claude-sonnet-5","top_p":0.5,"system":"S","messages":[{"role":"user","content":"hi"}]}`)
	if status != 200 {
		t.Fatalf("应成功: %d", status)
	}
	m := map[string]any{}
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "claude-sonnet-5" {
		t.Fatalf("上游应收到剥离前缀的模型名: %v", m["model"])
	}
	if _, ok := m["top_p"]; ok {
		t.Fatal("上游不应收到 top_p")
	}
	system := fmt.Sprint(m["system"])
	if !strings.Contains(system, claudeFingerprint) || !strings.Contains(system, "S") {
		t.Fatalf("上游 system 应含指纹与原文: %s", system)
	}
	if r := got; !strings.Contains(string(r), `"model"`) {
		t.Fatal("body 异常")
	}
}

// ---------- 流式与计费 ----------

func TestSSEStreamAndBilling(t *testing.T) {
	proceed := make(chan struct{})
	fx := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":25,\"output_tokens\":1}}}\n\n")
		fl.Flush()
		<-proceed
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":42}}\n\n")
		fl.Flush()
	}, func(fx *fixture) { fx.addAccount(t, "a1") })
	key := fx.createKey(t, "k", store.APIKeyOpts{})

	req, _ := http.NewRequest(http.MethodPost, fx.gw.URL+"/v1/messages", strings.NewReader(
		`{"model":"claude-sonnet-5","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("应 200: %d", resp.StatusCode)
	}

	// 逐事件到达：relay 尚未发后续帧时已能读到 message_start
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	select {
	case first := <-lines:
		if first != "event: message_start" {
			t.Fatalf("首行应为 message_start 事件: %q", first)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSE 应逐事件到达（relay 未放行时不应等待全量）")
	}
	close(proceed)
	var rest strings.Builder
	for line := range lines {
		rest.WriteString(line + "\n")
	}
	if !strings.Contains(rest.String(), "message_delta") {
		t.Fatalf("应收到完整流: %s", rest.String())
	}

	// 计费：usage 从 SSE tee 提取，cost = (25*3 + 42*15)/1e6 = 0.000705
	fx.rec.Flush()
	logs, err := fx.st.QueryUsageLogs(store.UsageLogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("应有 1 条用量日志: %d", len(logs))
	}
	l := logs[0]
	if l.InputTokens != 25 || l.OutputTokens != 42 || l.Status != 200 {
		t.Fatalf("用量日志不符: %+v", l)
	}
	if want := 0.000705; l.Cost < want-1e-9 || l.Cost > want+1e-9 {
		t.Fatalf("cost 不符: got %f want %f", l.Cost, want)
	}
}

func TestNonStreamUsageBilling(t *testing.T) {
	fx := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c1","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":15,"completion_tokens":8}}`)
	}, func(fx *fixture) { fx.addAccount(t, "a1") })
	key := fx.createKey(t, "k", store.APIKeyOpts{})

	status, hdr, body := post(t, fx.gw.URL+"/v1/chat/completions", key, chatBody)
	if status != 200 || !strings.Contains(string(body), "hi") {
		t.Fatalf("非流式应透传: %d %s", status, body)
	}
	if !strings.Contains(hdr.Get("Content-Type"), "application/json") {
		t.Fatalf("content-type 应透传: %v", hdr)
	}
	fx.rec.Flush()
	logs, _ := fx.st.QueryUsageLogs(store.UsageLogFilter{})
	if len(logs) != 1 || logs[0].InputTokens != 15 || logs[0].OutputTokens != 8 {
		t.Fatalf("非流式 usage 应被解析: %+v", logs)
	}
	// kimi-k3 未定价，cost=0
	if logs[0].Cost != 0 {
		t.Fatalf("未定价模型 cost 应 0: %f", logs[0].Cost)
	}
}

// ---------- models 与 health ----------

func TestModelsPassthrough(t *testing.T) {
	fx := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[{"id":"claude-real-1","object":"model"}]}`)
	}, func(fx *fixture) { fx.addAccount(t, "a1") })
	key := fx.createKey(t, "k", store.APIKeyOpts{})
	req, _ := http.NewRequest(http.MethodGet, fx.gw.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "claude-real-1") {
		t.Fatalf("应透传 relay /v1/models: %d %s", resp.StatusCode, b)
	}
}

func TestModelsStaticFallback(t *testing.T) {
	fx := newFixture(t, okRelay, nil) // 空池
	key := fx.createKey(t, "k", store.APIKeyOpts{})
	req, _ := http.NewRequest(http.MethodGet, fx.gw.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Object != "list" || len(m.Data) != len(staticModels) {
		t.Fatalf("静态清单不符: %+v", m)
	}
	ids := map[string]bool{}
	for _, d := range m.Data {
		ids[d.ID] = true
	}
	for _, want := range staticModels {
		if !ids[want] {
			t.Fatalf("静态清单缺 %s", want)
		}
	}
}

func TestHealth(t *testing.T) {
	fx := newFixture(t, okRelay, func(fx *fixture) { fx.addAccount(t, "a1") })
	resp, err := http.Get(fx.gw.URL + "/health") // 无鉴权
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || m["status"] != "ok" || m["accounts"].(float64) != 1 || m["available"].(float64) != 1 {
		t.Fatalf("health 不符: %d %v", resp.StatusCode, m)
	}
}
