package gateway

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"mirasim2api/internal/store"
)

const (
	// defaultKeyConcurrency 是 key 未配置并发上限时的默认值。
	defaultKeyConcurrency = 5
	// defaultKeySlotTimeout 是等待并发槽的默认超时，超时拒绝（429）。
	defaultKeySlotTimeout = 30 * time.Second
	rateWindow            = time.Minute
)

// extractAPIKey 从 Authorization: Bearer sk-... 或 x-api-key 取 key。
func extractAPIKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if v, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(v)
		}
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

// keySlotter 是每 key 并发槽（计数信号量）。
type keySlotter struct {
	mu    sync.Mutex
	chans map[string]chan struct{}
}

func newKeySlotter() *keySlotter { return &keySlotter{chans: map[string]chan struct{}{}} }

func (s *keySlotter) chanFor(keyID string, capacity int) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.chans[keyID]
	if !ok || cap(ch) != capacity {
		ch = make(chan struct{}, capacity)
		s.chans[keyID] = ch
	}
	return ch
}

// acquire 占槽；ctx 取消或超时返回 false。
func (s *keySlotter) acquire(ctx context.Context, keyID string, capacity int, timeout time.Duration) (release func(), ok bool) {
	ch := s.chanFor(keyID, capacity)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, true
	case <-ctx.Done():
		return nil, false
	case <-timer.C:
		return nil, false
	}
}

// rateLimiter 是每 key 每分钟滑动窗口限流器。
type rateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{hits: map[string][]time.Time{}} }

// allow 记录一次命中；超出 rpm 时返回 false 与重试等待时长。
func (l *rateLimiter) allow(keyID string, rpm int, now time.Time) (retryAfter time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-rateWindow)
	hits := l.hits[keyID]
	kept := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= rpm {
		retryAfter = rateWindow - now.Sub(kept[0])
		l.hits[keyID] = kept
		return retryAfter, false
	}
	l.hits[keyID] = append(kept, now)
	return 0, true
}

// authenticate 校验 sk- key：存在、enabled、未过期、模型在白名单内。
// ok=false 表示已写好错误响应，调用方直接 return。
// cfg.GatewayKeyRequired=false 且未带 key 时放行（key=nil, ok=true）。
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request, endpoint, model string) (key *store.APIKey, ok bool) {
	plaintext := extractAPIKey(r)
	if plaintext == "" {
		if !h.cfg.GatewayKeyRequired {
			return nil, true
		}
		writeAuthError(w, endpoint, "missing api key: use 'Authorization: Bearer sk-...' or 'x-api-key'")
		return nil, false
	}
	key, err := h.st.GetAPIKeyByPlaintext(plaintext)
	if err != nil {
		writeAuthError(w, endpoint, "invalid api key")
		return nil, false
	}
	if !key.Enabled {
		writeAuthError(w, endpoint, "api key is disabled")
		return nil, false
	}
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		writeAuthError(w, endpoint, "api key has expired")
		return nil, false
	}
	if model != "" && len(key.ModelAllowlist) > 0 {
		allowed := false
		for _, m := range key.ModelAllowlist {
			if m == model {
				allowed = true
				break
			}
		}
		if !allowed {
			writeError(w, endpoint, http.StatusForbidden, "permission_error",
				"model "+model+" is not allowed for this api key")
			return nil, false
		}
	}
	return key, true
}

// acquireKeyLimits 占并发槽并过限流；失败时已写好 429，返回 nil。
// 返回的 release 必须在请求结束时调用。
func (h *Handler) acquireKeyLimits(w http.ResponseWriter, r *http.Request, endpoint string, key *store.APIKey) (release func()) {
	if key == nil {
		return func() {}
	}
	conc := key.Concurrency
	if conc <= 0 {
		conc = defaultKeyConcurrency
	}
	timeout := h.keySlotTimeout
	if timeout <= 0 {
		timeout = defaultKeySlotTimeout
	}
	rel, ok := h.slots.acquire(r.Context(), key.ID, conc, timeout)
	if !ok {
		if r.Context().Err() == nil {
			writeError(w, endpoint, http.StatusTooManyRequests, "rate_limit_error",
				"too many concurrent requests for this api key")
		}
		return nil
	}
	if key.RateLimitRPM > 0 {
		if retryAfter, ok := h.rates.allow(key.ID, key.RateLimitRPM, time.Now()); !ok {
			rel()
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			writeError(w, endpoint, http.StatusTooManyRequests, "rate_limit_error",
				"rate limit exceeded: max "+strconv.Itoa(key.RateLimitRPM)+" requests per minute")
			return nil
		}
	}
	return rel
}
