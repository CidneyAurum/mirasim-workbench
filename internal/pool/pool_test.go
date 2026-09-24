package pool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"
	"time"

	"mirasim2api/internal/mirasim"
	"mirasim2api/internal/store"
)

type mockFetcher struct {
	limits *Limits
	err    error
	calls  atomic.Int64
}

func (m *mockFetcher) FetchLimits(ctx context.Context) (*Limits, error) {
	m.calls.Add(1)
	return m.limits, m.err
}

func newTestPool(t *testing.T) *Pool {
	t.Helper()
	dir := t.TempDir()
	key := make([]byte, 32)
	st, err := store.Open(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, func(acc *store.Account) (*mirasim.Client, error) {
		priv, err := mirasim.GenerateDeviceKey()
		if err != nil {
			return nil, err
		}
		return mirasim.NewClient(acc.RefreshToken, priv, "", "", "", "", "")
	})
}

func newEntry(id string, fetcher LimitFetcher) *Entry {
	return &Entry{
		Account: &store.Account{ID: id, Email: id + "@x.com", Enabled: true},
		Fetcher: fetcher,
	}
}

func TestWorstUsedRatio(t *testing.T) {
	l := &Limits{Windows: []Window{{Used: 10, Budget: 100}, {Used: 80, Budget: 100}}}
	if r := l.WorstUsedRatio(); r != 0.8 {
		t.Fatalf("worst 应为 0.8: %f", r)
	}
	var nilLimits *Limits
	if nilLimits.WorstUsedRatio() != 0 {
		t.Fatal("无快照应为 0")
	}
	if (&Limits{Windows: []Window{{Used: 5, Budget: 0}}}).WorstUsedRatio() != 0 {
		t.Fatal("budget=0 is unlimited in the official client")
	}
	if (&Limits{}).WorstUsedRatio() != 0 {
		t.Fatal("无窗口应为 0")
	}
}

// TestWindowJSONFloat 锁定：上游 /v1/limits 的 used/budget 可能是小数
//（如 150.5739624），Window 字段须用 float64 才能解析成功。
func TestWindowJSONFloat(t *testing.T) {
	raw := `{"windows":[{"used":150.5739624,"budget":5000},{"used":17,"budget":100}],"suspended":false}`
	var body struct {
		Windows   []Window `json:"windows"`
		Suspended bool     `json:"suspended"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(body.Windows) != 2 {
		t.Fatalf("窗口数不符: %d", len(body.Windows))
	}
	// 浮点 used 应保留小数部分。
	if got := body.Windows[0].Used; got < 150.5 || got > 150.6 {
		t.Fatalf("浮点 used 解析错误: %v", got)
	}
	if body.Windows[1].Used != 17 || body.Windows[1].Budget != 100 {
		t.Fatalf("整数字段解析错误: %+v", body.Windows[1])
	}
}

func TestPickWeightedRandom(t *testing.T) {
	p := newTestPool(t)
	p.SetRand(rand.New(rand.NewSource(42)))
	fat := newEntry("fat", &mockFetcher{})   // 剩余 90% → 权重 0.9
	thin := newEntry("thin", &mockFetcher{}) // 剩余 10% → 权重 0.1
	fat.mu.Lock()
	fat.limits = &Limits{Windows: []Window{{Used: 10, Budget: 100}}}
	fat.mu.Unlock()
	thin.mu.Lock()
	thin.limits = &Limits{Windows: []Window{{Used: 90, Budget: 100}}}
	thin.mu.Unlock()
	p.Add(fat)
	p.Add(thin)

	counts := map[string]int{}
	for range 2000 {
		e := p.Pick(nil, "")
		if e == nil {
			t.Fatal("无可选账号")
		}
		counts[e.Account.ID]++
	}
	ratio := float64(counts["fat"]) / 2000
	if ratio < 0.82 || ratio > 0.98 {
		t.Fatalf("加权分布不合理: fat=%d thin=%d", counts["fat"], counts["thin"])
	}

	// 排除后只剩另一个
	e := p.Pick(map[string]bool{"fat": true}, "")
	if e == nil || e.Account.ID != "thin" {
		t.Fatalf("exclude 未生效: %v", e)
	}
	// 全部排除 → nil
	if e := p.Pick(map[string]bool{"fat": true, "thin": true}, ""); e != nil {
		t.Fatal("全部排除应返回 nil")
	}
}

func TestPickUsableRules(t *testing.T) {
	p := newTestPool(t)
	p.SetRand(rand.New(rand.NewSource(1)))
	ok := newEntry("ok", &mockFetcher{})
	disabled := newEntry("disabled", &mockFetcher{})
	disabled.Account.Enabled = false
	suspended := newEntry("suspended", &mockFetcher{})
	suspended.mu.Lock()
	suspended.limits = &Limits{Suspended: true}
	suspended.mu.Unlock()
	exhausted := newEntry("exhausted", &mockFetcher{})
	exhausted.mu.Lock()
	exhausted.limits = &Limits{Windows: []Window{{Used: 100, Budget: 100}}}
	exhausted.mu.Unlock()
	p.Add(ok)
	p.Add(disabled)
	p.Add(suspended)
	p.Add(exhausted)

	for range 50 {
		e := p.Pick(nil, "")
		if e == nil || e.Account.ID != "ok" {
			t.Fatalf("应只选中 ok: %v", e)
		}
	}
}

func TestStickySession(t *testing.T) {
	p := newTestPool(t)
	p.SetRand(rand.New(rand.NewSource(7)))
	a := newEntry("a", &mockFetcher{})
	b := newEntry("b", &mockFetcher{})
	p.Add(a)
	p.Add(b)

	first := p.Pick(nil, "session-1")
	for range 20 {
		if e := p.Pick(nil, "session-1"); e != first {
			t.Fatalf("粘性未命中: %v != %v", e, first)
		}
	}
	// 粘性账号失效 → 回退到另一个并更新映射
	p.ReportFailure(first, fmt.Errorf("boom")) // 15s 冷却
	second := p.Pick(nil, "session-1")
	if second == first {
		t.Fatal("失效后应回退到其他账号")
	}
	for range 5 {
		if e := p.Pick(nil, "session-1"); e != second {
			t.Fatal("回退后粘性应指向新账号")
		}
	}
}

func TestStickyExpireAndLRU(t *testing.T) {
	p := newTestPool(t)
	p.SetRand(rand.New(rand.NewSource(3)))
	p.Add(newEntry("a", &mockFetcher{}))
	p.Add(newEntry("b", &mockFetcher{}))

	base := time.Now()
	current := base
	p.SetNow(func() time.Time { return current })
	first := p.Pick(nil, "s1")

	// 过期后应重新随机选取（映射被清理后重建）
	current = base.Add(stickyTTL + time.Second)
	second := p.Pick(nil, "s1")
	if second == nil {
		t.Fatal("过期后仍应能选号")
	}
	p.mu.Lock()
	se, ok := p.sticky["s1"]
	p.mu.Unlock()
	if !ok || !se.expiresAt.After(current) {
		t.Fatal("过期项应被重建为新 TTL")
	}
	_ = first

	// LRU 上限
	p2 := newTestPool(t)
	p2.SetRand(rand.New(rand.NewSource(3)))
	p2.Add(newEntry("a", &mockFetcher{}))
	for i := range stickyMaxEntries + 100 {
		p2.Pick(nil, fmt.Sprintf("k%d", i))
	}
	p2.mu.Lock()
	n := len(p2.sticky)
	p2.mu.Unlock()
	if n > stickyMaxEntries {
		t.Fatalf("粘性映射超上限: %d", n)
	}
}

func TestReportFailureBackoffAnd5xx(t *testing.T) {
	p := newTestPool(t)
	base := time.Now()
	current := base
	p.SetNow(func() time.Time { return current })
	e := newEntry("a", &mockFetcher{})
	p.Add(e)

	// 5xx 不熔断
	p.ReportFailure(e, &mirasim.HTTPError{StatusCode: 503, Body: "model_capacity_exhausted"})
	p.ReportFailure(e, &mirasim.HTTPError{StatusCode: 500})
	if !e.usable(current) {
		t.Fatal("5xx 不应触发冷却")
	}

	// 普通错误：15s → 30s（2 倍递增）
	p.ReportFailure(e, fmt.Errorf("boom"))
	if e.usable(current.Add(14*time.Second)) || !e.usable(current.Add(16*time.Second)) {
		t.Fatal("第一次退避应为 15s")
	}
	p.ReportFailure(e, fmt.Errorf("boom"))
	if e.usable(current.Add(29*time.Second)) || !e.usable(current.Add(31*time.Second)) {
		t.Fatal("第二次退避应为 30s")
	}

	// 成功后清退避
	p.ReportSuccess(e)
	if !e.usable(current) {
		t.Fatal("ReportSuccess 应清除冷却")
	}
	e.mu.Lock()
	if e.failures != 0 {
		t.Fatal("ReportSuccess 应清零失败计数")
	}
	e.mu.Unlock()
}

func TestReportFailureBackoffCap(t *testing.T) {
	p := newTestPool(t)
	base := time.Now()
	current := base
	p.SetNow(func() time.Time { return current })
	e := newEntry("a", &mockFetcher{})
	p.Add(e)
	for range 20 {
		p.ReportFailure(e, fmt.Errorf("boom"))
	}
	e.mu.Lock()
	cd := e.cooldownUntil
	e.mu.Unlock()
	if cd.Sub(current) != maxBackoff {
		t.Fatalf("退避应封顶 %v, got %v", maxBackoff, cd.Sub(current))
	}
}

func TestRefreshLimitsForce(t *testing.T) {
	p := newTestPool(t)
	mf := &mockFetcher{limits: &Limits{Windows: []Window{{Used: 1, Budget: 10}}}}
	e := newEntry("a", mf)
	p.Add(e)
	ctx := context.Background()

	if err := p.RefreshLimits(ctx, e, false); err != nil {
		t.Fatal(err)
	}
	if mf.calls.Load() != 1 {
		t.Fatal("首次应拉取")
	}
	if e.LimitsSnapshot() == nil || e.LimitsSnapshot().WorstUsedRatio() != 0.1 {
		t.Fatal("快照未写入")
	}
	// 新鲜且非 force → 跳过
	if err := p.RefreshLimits(ctx, e, false); err != nil {
		t.Fatal(err)
	}
	if mf.calls.Load() != 1 {
		t.Fatal("新鲜快照不应重复拉取")
	}
	// force → 再拉
	if err := p.RefreshLimits(ctx, e, true); err != nil {
		t.Fatal(err)
	}
	if mf.calls.Load() != 2 {
		t.Fatal("force 应强制拉取")
	}
}

func TestReportFailure401TriggersForceRefresh(t *testing.T) {
	p := newTestPool(t)
	mf := &mockFetcher{limits: &Limits{}}
	e := newEntry("a", mf)
	p.Add(e)
	// 先灌入新鲜快照
	if err := p.RefreshLimits(context.Background(), e, false); err != nil {
		t.Fatal(err)
	}
	before := mf.calls.Load()
	p.ReportFailure(e, &mirasim.HTTPError{StatusCode: 401})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mf.calls.Load() > before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("401 应异步强制刷新 limits")
}

func TestTryAcquire(t *testing.T) {
	e := newEntry("a", &mockFetcher{})
	if !e.TryAcquire(2) || !e.TryAcquire(2) {
		t.Fatal("应能占两个槽")
	}
	if e.TryAcquire(2) {
		t.Fatal("超过上限应失败")
	}
	e.Release()
	if !e.TryAcquire(2) {
		t.Fatal("释放后应能再占")
	}
	if e.Inflight.Load() != 2 {
		t.Fatalf("Inflight 计数: %d", e.Inflight.Load())
	}
	// max=0 不限
	e2 := newEntry("b", &mockFetcher{})
	for range 100 {
		if !e2.TryAcquire(0) {
			t.Fatal("max=0 应不限")
		}
	}
}

func TestStatsWithPlan(t *testing.T) {
	p := newTestPool(t)
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"plan":"pro","plan_exp":1760000000,"email":"a@x.com"}`))
	token := "h." + payload + ".s"
	priv, _ := mirasim.GenerateDeviceKey()
	client, err := mirasim.NewClient(token, priv, "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	e := &Entry{
		Account: &store.Account{ID: "a", Email: "a@x.com", Enabled: true},
		Client:  client,
		Fetcher: &mockFetcher{},
	}
	e.mu.Lock()
	e.limits = &Limits{Windows: []Window{{Used: 25, Budget: 100}}}
	e.mu.Unlock()
	p.Add(e)

	stats := p.Stats()
	if len(stats) != 1 {
		t.Fatalf("stats 行数: %d", len(stats))
	}
	st := stats[0]
	if st.Plan != "pro" || st.PlanExp != 1760000000 {
		t.Fatalf("plan 解析失败: %+v", st)
	}
	if st.WorstUsedRatio != 0.25 || !st.LimitsFetched || st.Suspended {
		t.Fatalf("limits 状态不符: %+v", st)
	}
	if !st.Enabled || st.ID != "a" {
		t.Fatalf("基础字段不符: %+v", st)
	}
}

func TestReloadPreservesRuntimeState(t *testing.T) {
	p := newTestPool(t)
	// 通过 store 加号走完整 Reload 路径
	if _, err := p.st.AddAccount("u@x.com", "n", "github", "rt-1"); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	var e *Entry
	for _, cand := range p.Entries() {
		e = cand
	}
	if e == nil {
		t.Fatal("Reload 后应有 1 个 Entry")
	}
	p.ReportFailure(e, fmt.Errorf("boom")) // 进入冷却
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	e2 := p.Get(e.Account.ID)
	e2.mu.Lock()
	cooled := !e2.cooldownUntil.IsZero()
	e2.mu.Unlock()
	if !cooled {
		t.Fatal("Reload 不应丢运行时冷却状态")
	}

	// 删除账号后 Reload 应移除
	if err := p.st.DeleteAccount(e.Account.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(p.Entries()) != 0 {
		t.Fatal("删除后应移除 Entry")
	}
}
