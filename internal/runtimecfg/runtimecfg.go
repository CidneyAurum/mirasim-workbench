// Package runtimecfg 提供「运行时设置优先，环境变量兜底」的共享设置访问器：
// 管理端写入 store.settings 即刻生效（gateway / billing 每次按需读取，
// 不缓存到启动时）；键未入库时回落到启动时捕获的环境变量，再到内置默认。
package runtimecfg

import (
	"strconv"

	"mirasim2api/internal/store"
)

// 可热更设置键（store.settings 的 key）。
const (
	KeyUpstreamProxy     = "upstream_proxy"      // 出站 HTTP CONNECT 代理
	KeyCloakMode         = "claude_cloak_mode"   // relaxed / strict
	KeyCapacityRetries   = "capacity_retries"    // 503 model_capacity_exhausted 重试次数
	KeyCapacityBackoffMS = "capacity_backoff_ms" // 容量重试退避基数（毫秒）
	KeyModelPrices       = "model_prices"        // 模型价格表 JSON（billing 使用）
	KeyRateMultiplier    = "rate_multiplier"     // 计费倍率（billing 使用）
)

// EnvKeys 是设置键到兜底环境变量名的映射（model_prices/rate_multiplier 无环境变量）。
var EnvKeys = map[string]string{
	KeyUpstreamProxy:     "UPSTREAM_PROXY",
	KeyCloakMode:         "CLAUDE_CLOAK_MODE",
	KeyCapacityRetries:   "CAPACITY_RETRIES",
	KeyCapacityBackoffMS: "CAPACITY_BACKOFF_MS",
}

// 设置值来源（管理端展示用）。
const (
	SourceDatabase = "database"
	SourceEnv      = "env"
	SourceDefault  = "default"
)

// Accessor 是运行时设置访问器（线程安全；底层 sql.DB 天然并发安全，直读不缓存）。
type Accessor struct {
	st       *store.Store
	env      map[string]string // 启动时显式设置的环境变量快照
	defaults map[string]string // 内置默认值
}

// New 创建访问器。env 只应包含显式设置过的环境变量（用于区分 env 与 default 来源）。
func New(st *store.Store, env, defaults map[string]string) *Accessor {
	return &Accessor{st: st, env: env, defaults: defaults}
}

// Get 读设置：数据库 → 环境变量 → 内置默认。键不存在且无默认时返回空串。
func (a *Accessor) Get(key string) string {
	if a.st != nil {
		if v, err := a.st.GetSetting(key); err == nil && v != "" {
			return v
		}
	}
	if v := a.env[key]; v != "" {
		return v
	}
	return a.defaults[key]
}

// GetInt 读整数设置；解析失败返回 def。
func (a *Accessor) GetInt(key string, def int) int {
	v := a.Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Source 报告当前生效值的来源：database / env / default。
func (a *Accessor) Source(key string) string {
	if a.st != nil {
		if v, err := a.st.GetSetting(key); err == nil && v != "" {
			return SourceDatabase
		}
	}
	if a.env[key] != "" {
		return SourceEnv
	}
	return SourceDefault
}

// Default 返回内置默认值。
func (a *Accessor) Default(key string) string { return a.defaults[key] }
