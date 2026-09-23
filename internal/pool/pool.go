// Package pool 管理 Mirasim 账号池：额度加权选号、会话粘性、熔断冷却、配额轮询。
// 选号策略见 docs/PROTOCOL.md「选号与配额经验」。
package pool

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mirasim2api/internal/mirasim"
	"mirasim2api/internal/store"
)

const (
	stickyTTL        = 10 * time.Minute // 会话粘性 TTL
	stickyMaxEntries = 1000             // 粘性映射上限（LRU）
	limitsTTL        = 10 * time.Minute // /v1/limits 快照保鲜期
	limitsLoopPeriod = 10 * time.Minute // 后台轮询周期
	baseBackoff      = 15 * time.Second // 退避起点：15s×2^(n-1)
	maxBackoff       = 10 * time.Minute // 退避上限
	minWeight        = 0.05             // 保底权重
)

// Window 是 /v1/limits 返回的一个配额窗口。
// Used/Budget 用 float64：上游实测会返回小数（如 used=150.5739624）。
type Window struct {
	Used   float64 `json:"used"`
	Budget float64 `json:"budget"`
}

// Limits 是账号配额快照。
type Limits struct {
	Windows   []Window
	Suspended bool
	FetchedAt time.Time
}

// WorstUsedRatio 取最紧张窗口的已用比例 max(used/budget)；无快照返回 0。
func (l *Limits) WorstUsedRatio() float64 {
	if l == nil {
		return 0
	}
	worst := 0.0
	for _, w := range l.Windows {
		var r float64
		switch {
		case w.Budget > 0:
			r = w.Used / w.Budget
		case w.Used > 0:
			r = 1
		}
		if r > worst {
			worst = r
		}
	}
	return worst
}

// LimitFetcher 拉取账号配额（生产实现走 relay，测试注入 mock）。
type LimitFetcher interface {
	FetchLimits(ctx context.Context) (*Limits, error)
}

// relayLimitFetcher 用签名请求调 GET {relay}/v1/limits。
type relayLimitFetcher struct {
	client *mirasim.Client
}

func (f *relayLimitFetcher) FetchLimits(ctx context.Context) (*Limits, error) {
	resp, err := f.client.DoSignedJSON(ctx, "GET", "/v1/limits", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Windows   []Window `json:"windows"`
		Suspended bool     `json:"suspended"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("pool: limits 响应解析失败: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, &mirasim.HTTPError{StatusCode: resp.StatusCode, Body: "limits"}
	}
	return &Limits{Windows: body.Windows, Suspended: body.Suspended, FetchedAt: time.Now()}, nil
}

// Entry 是池中的一个账号。
type Entry struct {
	Account  *store.Account
	Client   *mirasim.Client
	Fetcher  LimitFetcher
	Inflight atomic.Int64

	mu            sync.Mutex
	failures      int
	cooldownUntil time.Time
	limits        *Limits
	lastError     string // 最近一次失败的摘要（供管理端展示）
}

// TryAcquire 尝试占一个并发槽；max<=0 表示不限。
func (e *Entry) TryAcquire(max int) bool {
	for {
		cur := e.Inflight.Load()
		if max > 0 && cur >= int64(max) {
			return false
		}
		if e.Inflight.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// Release 释放并发槽。
func (e *Entry) Release() { e.Inflight.Add(-1) }

// LimitsSnapshot 返回当前配额快照（可能为 nil）。
func (e *Entry) LimitsSnapshot() *Limits {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.limits
}

// usable 判定：enabled、不在冷却、limits 非 suspended、worstUsedRatio < 1（无快照可先选）。
func (e *Entry) usable(now time.Time) bool {
	if !e.Account.Enabled {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if now.Before(e.cooldownUntil) {
		return false
	}
	if e.limits != nil {
		if e.limits.Suspended || e.limits.WorstUsedRatio() >= 1 {
			return false
		}
	}
	return true
}

// weight = max(0.05, 1 - worstUsedRatio)。
func (e *Entry) weight() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	w := 1 - e.limits.WorstUsedRatio()
	if w < minWeight {
		w = minWeight
	}
	return w
}

type stickyEntry struct {
	accountID string
	expiresAt time.Time
	elem      *list.Element
}

// Pool 是账号池。
type Pool struct {
	st        *store.Store
	newClient func(acc *store.Account) (*mirasim.Client, error)

	mu      sync.Mutex
	entries map[string]*Entry
	sticky  map[string]*stickyEntry
	lru     *list.List // 队首最久未用，元素为 affinityKey
	rnd     *rand.Rand
	now     func() time.Time
}

// New 创建账号池并加载账号。newClient 为每个账号构造协议客户端（共享设备身份）。
func New(st *store.Store, newClient func(acc *store.Account) (*mirasim.Client, error)) *Pool {
	p := &Pool{
		st:        st,
		newClient: newClient,
		entries:   map[string]*Entry{},
		sticky:    map[string]*stickyEntry{},
		lru:       list.New(),
		rnd:       rand.New(rand.NewSource(time.Now().UnixNano())),
		now:       time.Now,
	}
	_ = p.Reload()
	return p
}

// SetRand 注入确定性随机源（测试用）。
func (p *Pool) SetRand(r *rand.Rand) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rnd = r
}

// SetNow 注入时钟（测试用）。
func (p *Pool) SetNow(f func() time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = f
}

// Reload 从 store 重新加载账号：新增补 Entry、删除移除 Entry、
// 已存在的保留运行时配额/冷却状态。
func (p *Pool) Reload() error {
	accounts, err := p.st.ListAccounts()
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]bool{}
	for _, acc := range accounts {
		seen[acc.ID] = true
		if e, ok := p.entries[acc.ID]; ok {
			e.Account = acc // 刷新 enabled 等字段，保留运行时状态
			continue
		}
		client, err := p.newClient(acc)
		if err != nil {
			return fmt.Errorf("pool: 账号 %s 客户端创建失败: %w", acc.ID, err)
		}
		accID := acc.ID
		client.OnRotate = func(newToken string) {
			_ = p.st.UpdateAccountToken(accID, newToken)
		}
		p.entries[acc.ID] = &Entry{Account: acc, Client: client, Fetcher: &relayLimitFetcher{client}}
	}
	for id := range p.entries {
		if !seen[id] {
			delete(p.entries, id)
		}
	}
	return nil
}

// Add 直接放入一个 Entry（测试注入 mock 用）。
func (p *Pool) Add(e *Entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries[e.Account.ID] = e
}

// Get 按账号 ID 取 Entry。
func (p *Pool) Get(accountID string) *Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.entries[accountID]
}

// Entries 返回全部 Entry 的快照。
func (p *Pool) Entries() []*Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*Entry, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, e)
	}
	return out
}

// Pick 选号：会话粘性优先，其次额度加权随机。
// exclude 中的账号 ID 不参与；affinityKey 为空则跳过粘性。
func (p *Pool) Pick(exclude map[string]bool, affinityKey string) *Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()

	if affinityKey != "" {
		if se, ok := p.sticky[affinityKey]; ok {
			if now.After(se.expiresAt) {
				p.removeSticky(affinityKey)
			} else if e, ok := p.entries[se.accountID]; ok && !exclude[se.accountID] && e.usable(now) {
				p.lru.MoveToBack(se.elem)
				se.expiresAt = now.Add(stickyTTL)
				return e
			}
			// 失效（账号不可用/被排除）则自动回退到加权随机
		}
	}

	var candidates []*Entry
	var total float64
	for _, e := range p.entries {
		if exclude[e.Account.ID] || !e.usable(now) {
			continue
		}
		candidates = append(candidates, e)
		total += e.weight()
	}
	if len(candidates) == 0 {
		return nil
	}
	r := p.rnd.Float64() * total
	var picked *Entry
	for _, e := range candidates {
		r -= e.weight()
		if r <= 0 {
			picked = e
			break
		}
	}
	if picked == nil {
		picked = candidates[len(candidates)-1]
	}
	if affinityKey != "" {
		p.setSticky(affinityKey, picked.Account.ID, now)
	}
	return picked
}

func (p *Pool) setSticky(key, accountID string, now time.Time) {
	if se, ok := p.sticky[key]; ok {
		se.accountID = accountID
		se.expiresAt = now.Add(stickyTTL)
		p.lru.MoveToBack(se.elem)
		return
	}
	for len(p.sticky) >= stickyMaxEntries {
		front := p.lru.Front()
		if front == nil {
			break
		}
		p.removeSticky(front.Value.(string))
	}
	elem := p.lru.PushBack(key)
	p.sticky[key] = &stickyEntry{accountID: accountID, expiresAt: now.Add(stickyTTL), elem: elem}
}

func (p *Pool) removeSticky(key string) {
	if se, ok := p.sticky[key]; ok {
		p.lru.Remove(se.elem)
		delete(p.sticky, key)
	}
}

// ReportSuccess 清退避；limits 快照过期则异步刷新。
func (p *Pool) ReportSuccess(e *Entry) {
	e.mu.Lock()
	e.failures = 0
	e.cooldownUntil = time.Time{}
	e.lastError = ""
	stale := e.limits == nil || time.Since(e.limits.FetchedAt) > limitsTTL
	e.mu.Unlock()
	if stale {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = p.RefreshLimits(ctx, e, false)
		}()
	}
}

// ReportFailure 上报失败：HTTP status ≥500 不熔断（relay 故障）；
// 否则指数退避 15s×2^(n-1)，上限 10 分钟；401/403 强制刷一次 limits。
// 无论是否熔断都记录 lastError 供管理端展示。
func (p *Pool) ReportFailure(e *Entry, err error) {
	var httpErr *mirasim.HTTPError
	is5xx := errors.As(err, &httpErr) && httpErr.StatusCode >= 500
	e.mu.Lock()
	e.lastError = errSummary(err)
	if !is5xx {
		e.failures++
		backoff := baseBackoff << (e.failures - 1)
		if backoff > maxBackoff || backoff < 0 {
			backoff = maxBackoff
		}
		e.cooldownUntil = p.now().Add(backoff)
	}
	e.mu.Unlock()
	if httpErr != nil && (httpErr.StatusCode == 401 || httpErr.StatusCode == 403) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = p.RefreshLimits(ctx, e, true)
		}()
	}
}

// errSummary 压缩错误为单行短摘要（≤200 字符）。
func errSummary(err error) string {
	if err == nil {
		return ""
	}
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// RefreshLimits 拉取账号配额；快照新鲜且非 force 时跳过。
func (p *Pool) RefreshLimits(ctx context.Context, e *Entry, force bool) error {
	e.mu.Lock()
	fresh := e.limits != nil && time.Since(e.limits.FetchedAt) <= limitsTTL
	e.mu.Unlock()
	if fresh && !force {
		return nil
	}
	limits, err := e.Fetcher.FetchLimits(ctx)
	if err != nil {
		return err
	}
	if limits == nil {
		return errors.New("pool: limits 拉取返回空")
	}
	limits.FetchedAt = time.Now()
	e.mu.Lock()
	e.limits = limits
	e.mu.Unlock()
	return nil
}

// StartLimitsLoop 后台每 10 分钟轮询一轮全部账号配额，直到 ctx 取消。
func (p *Pool) StartLimitsLoop(ctx context.Context) {
	ticker := time.NewTicker(limitsLoopPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, e := range p.Entries() {
				ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
				_ = p.RefreshLimits(ctx2, e, false)
				cancel()
			}
		}
	}
}

// AccountStat 供管理端展示。
type AccountStat struct {
	ID             string
	Email          string
	Name           string
	Provider       string
	Enabled        bool
	Inflight       int64
	Failures       int
	CooldownUntil  time.Time
	WorstUsedRatio float64
	Suspended      bool
	LimitsFetched  bool
	LastError      string // 最近一次失败摘要（成功清零）
	Plan           string
	PlanExp        int64
}

// Stats 返回全部账号的运行时状态（plan/plan_exp 从 refresh token JWT 本地解出）。
func (p *Pool) Stats() []*AccountStat {
	var out []*AccountStat
	for _, e := range p.Entries() {
		e.mu.Lock()
		st := &AccountStat{
			ID:            e.Account.ID,
			Email:         e.Account.Email,
			Name:          e.Account.Name,
			Provider:      e.Account.Provider,
			Enabled:       e.Account.Enabled,
			Inflight:      e.Inflight.Load(),
			Failures:      e.failures,
			CooldownUntil: e.cooldownUntil,
			LastError:     e.lastError,
		}
		if e.limits != nil {
			st.WorstUsedRatio = e.limits.WorstUsedRatio()
			st.Suspended = e.limits.Suspended
			st.LimitsFetched = true
		}
		e.mu.Unlock()
		if claims := mirasim.JWTClaims(e.Client.RefreshToken()); claims != nil {
			if plan, ok := claims["plan"].(string); ok {
				st.Plan = plan
			}
			switch v := claims["plan_exp"].(type) {
			case float64:
				st.PlanExp = int64(v)
			case int64:
				st.PlanExp = v
			}
		}
		out = append(out, st)
	}
	return out
}
