package billing

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"mirasim2api/internal/store"
)

const (
	// flushInterval 与 flushBatch 控制批量落库：500ms 或满 20 条 flush。
	flushInterval = 500 * time.Millisecond
	flushBatch    = 20
	// pricesTTL 是价格表/倍率设置的缓存保鲜期。
	pricesTTL = 30 * time.Second

	settingModelPrices    = "model_prices"
	settingRateMultiplier = "rate_multiplier"
)

// ModelPrice 是模型单价（美元/百万 token）。
type ModelPrice struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Cached float64 `json:"cached"`
}

// defaultPrices 是内置默认价格表（美元/百万 token）；settings 的
// model_prices JSON 可覆盖。gpt/第三方模型未定价，记 0。
var defaultPrices = map[string]ModelPrice{
	"claude-opus-5":     {Input: 15, Output: 75, Cached: 1.5},
	"claude-opus-4-8":   {Input: 15, Output: 75, Cached: 1.5},
	"claude-sonnet-5":   {Input: 3, Output: 15, Cached: 0.3},
	"claude-fable-5":    {Input: 3, Output: 15, Cached: 0.3}, // 未公开定价，按 sonnet 档位
	"claude-haiku-4-5":  {Input: 1, Output: 5, Cached: 0.1},
	"gpt-6-astra":       {}, // 未定价
	"kimi-k3":           {}, // 未定价
	"deepseek-flash":    {}, // 未定价
	"deepseek-v4-flash": {}, // 未定价；deepseek-flash 的历史别名，仅留计费口径
	"glm-5.3-flash":     {}, // 未定价
}

// DefaultPrices 返回内置默认价格表的副本（管理端展示默认值用）。
func DefaultPrices() map[string]ModelPrice {
	out := make(map[string]ModelPrice, len(defaultPrices))
	for k, v := range defaultPrices {
		out[k] = v
	}
	return out
}

// Recorder 计算请求费用并批量落库（usage_logs + api_keys 冗余累计）。
type Recorder struct {
	st *store.Store

	mu             sync.Mutex
	buf            []*store.UsageLog
	prices         map[string]ModelPrice
	multiplier     float64
	pricesLoadedAt time.Time

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewRecorder 创建 Recorder 并启动后台 flush 循环。
func NewRecorder(st *store.Store) *Recorder {
	r := &Recorder{st: st, multiplier: 1, stop: make(chan struct{})}
	r.wg.Add(1)
	go r.loop()
	return r
}

// Record 计算 log.Cost 后入缓冲（线程安全）；满一批即触发 flush。
// usage 为 0 的失败请求也照常落日志，cost=0。
func (r *Recorder) Record(l *store.UsageLog) {
	r.mu.Lock()
	r.refreshPricesLocked()
	l.Cost = r.costLocked(l.Model, l.InputTokens, l.OutputTokens, l.CachedTokens)
	r.buf = append(r.buf, l)
	full := len(r.buf) >= flushBatch
	r.mu.Unlock()
	if full {
		r.Flush()
	}
}

// Cost 按当前价格表估算费用（供日志等即时展示用）。
func (r *Recorder) Cost(model string, input, output, cached int64) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshPricesLocked()
	return r.costLocked(model, input, output, cached)
}

func (r *Recorder) costLocked(model string, input, output, cached int64) float64 {
	p, ok := r.prices[model]
	if !ok {
		// 前缀匹配（如 claude-sonnet-5-20260923 → claude-sonnet-5），取最长匹配
		best := ""
		for name := range r.prices {
			if len(name) > len(best) && len(model) > len(name) && model[:len(name)] == name && model[len(name)] == '-' {
				best = name
			}
		}
		p = r.prices[best]
	}
	usd := (float64(input)*p.Input + float64(output)*p.Output + float64(cached)*p.Cached) / 1e6
	return usd * r.multiplier
}

func (r *Recorder) refreshPricesLocked() {
	if r.prices != nil && time.Since(r.pricesLoadedAt) < pricesTTL {
		return
	}
	prices := make(map[string]ModelPrice, len(defaultPrices))
	for k, v := range defaultPrices {
		prices[k] = v
	}
	multiplier := 1.0
	if raw, err := r.st.GetSetting(settingModelPrices); err == nil && raw != "" {
		var custom map[string]ModelPrice
		if json.Unmarshal([]byte(raw), &custom) == nil {
			for k, v := range custom {
				prices[k] = v
			}
		}
	}
	if raw, err := r.st.GetSetting(settingRateMultiplier); err == nil && raw != "" {
		if f, err := strconv.ParseFloat(raw, 64); err == nil && f > 0 {
			multiplier = f
		}
	}
	r.prices = prices
	r.multiplier = multiplier
	r.pricesLoadedAt = time.Now()
}

// Flush 把缓冲中的日志落库并累加 key 冗余统计。
func (r *Recorder) Flush() {
	r.mu.Lock()
	if len(r.buf) == 0 {
		r.mu.Unlock()
		return
	}
	batch := r.buf
	r.buf = nil
	r.mu.Unlock()
	for _, l := range batch {
		if _, err := r.st.AddUsageLog(l); err != nil {
			continue
		}
		if l.APIKeyID != "" {
			_ = r.st.AddUsage(l.APIKeyID, l.InputTokens, l.OutputTokens, l.Cost)
		}
	}
}

func (r *Recorder) loop() {
	defer r.wg.Done()
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.Flush()
		}
	}
}

// Close 停止后台循环并 flush 剩余缓冲。
func (r *Recorder) Close() {
	close(r.stop)
	r.wg.Wait()
	r.Flush()
}
