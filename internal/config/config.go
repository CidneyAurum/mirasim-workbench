// Package config 从环境变量加载配置（字段见 docs/DESIGN.md 配置表）。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mirasim2api/internal/mirasim"
)

// Config 是运行时配置。
type Config struct {
	Port               int
	Host               string
	DataDir            string
	MasterKey          []byte // 32 字节
	AdminPassword      string
	GatewayKeyRequired bool
	RelayURL           string
	AuthURL            string
	ClientVersion      string
	SealPubkey         string
	UpstreamProxy      string
	ClaudeCloakMode    string
	CapacityRetries    int
	CapacityBackoffMS  int
	// AccountMaxConcurrency 是每账号上游并发槽上限（0 由网关按默认 5 处理）。
	AccountMaxConcurrency int
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s 非法整数 %q", key, v)
	}
	return n, nil
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// Load 加载配置；MASTER_KEY 未设时在 DATA_DIR/master.key 生成 32 字节随机密钥。
func Load() (*Config, error) {
	dataDir := env("DATA_DIR", "./data")
	port, err := envInt("PORT", 8787)
	if err != nil {
		return nil, err
	}
	retries, err := envInt("CAPACITY_RETRIES", 2)
	if err != nil {
		return nil, err
	}
	backoffMS, err := envInt("CAPACITY_BACKOFF_MS", 1200)
	if err != nil {
		return nil, err
	}
	accConc, err := envInt("ACCOUNT_MAX_CONCURRENCY", 5)
	if err != nil {
		return nil, err
	}
	key, err := LoadOrCreateMasterKey(dataDir, os.Getenv("MASTER_KEY"))
	if err != nil {
		return nil, err
	}
	return &Config{
		Port:                  port,
		Host:                  env("HOST", "0.0.0.0"),
		DataDir:               dataDir,
		MasterKey:             key,
		AdminPassword:         os.Getenv("ADMIN_PASSWORD"),
		GatewayKeyRequired:    envBool("GATEWAY_KEY_REQUIRED", true),
		RelayURL:              env("RELAY_URL", mirasim.DefaultRelayURL),
		AuthURL:               env("AUTH_URL", mirasim.DefaultAuthURL),
		ClientVersion:         env("MIRASIM_CLIENT_VERSION", mirasim.DefaultClientVersion),
		SealPubkey:            env("MIRASIM_SEAL_PUBKEY", mirasim.DefaultSealPubkey),
		UpstreamProxy:         env("UPSTREAM_PROXY", ""),
		ClaudeCloakMode:       env("CLAUDE_CLOAK_MODE", "relaxed"),
		CapacityRetries:       retries,
		CapacityBackoffMS:     backoffMS,
		AccountMaxConcurrency: accConc,
	}, nil
}

// LoadOrCreateMasterKey 读取 hex 编码的 master key；envKey 非空则优先，
// 否则读 DATA_DIR/master.key，不存在则生成 32 字节随机密钥并以 0600 落盘。
func LoadOrCreateMasterKey(dataDir, envKey string) ([]byte, error) {
	if v := strings.TrimSpace(envKey); v != "" {
		key, err := hex.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("config: MASTER_KEY 须为 hex 字符串: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("config: MASTER_KEY 须为 64 位 hex（32 字节），当前 %d 字节", len(key))
		}
		return key, nil
	}
	path := filepath.Join(dataDir, "master.key")
	if raw, err := os.ReadFile(path); err == nil {
		key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("config: master.key 解码失败: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("config: master.key 长度非法（%d 字节）", len(key))
		}
		return key, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}
