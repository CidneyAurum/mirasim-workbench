package config

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	for _, k := range []string{"PORT", "HOST", "MASTER_KEY", "ADMIN_PASSWORD", "GATEWAY_KEY_REQUIRED",
		"RELAY_URL", "AUTH_URL", "MIRASIM_CLIENT_VERSION", "MIRASIM_SEAL_PUBKEY", "UPSTREAM_PROXY",
		"CLAUDE_CLOAK_MODE", "CAPACITY_RETRIES", "CAPACITY_BACKOFF_MS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8787 || cfg.Host != "0.0.0.0" {
		t.Fatalf("默认监听不符: %v:%v", cfg.Host, cfg.Port)
	}
	if cfg.ClientVersion != "0.0.322" || cfg.ClaudeCloakMode != "relaxed" {
		t.Fatalf("默认值不符: %+v", cfg)
	}
	if cfg.CapacityRetries != 2 || cfg.CapacityBackoffMS != 1200 || !cfg.GatewayKeyRequired {
		t.Fatalf("默认值不符: %+v", cfg)
	}
	if len(cfg.MasterKey) != 32 {
		t.Fatalf("master key 应为 32 字节: %d", len(cfg.MasterKey))
	}
}

func TestLoadFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("PORT", "9999")
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("MASTER_KEY", hex.EncodeToString(make([]byte, 32)))
	t.Setenv("ADMIN_PASSWORD", "pw")
	t.Setenv("RELAY_URL", "http://relay.local")
	t.Setenv("AUTH_URL", "http://auth.local")
	t.Setenv("MIRASIM_CLIENT_VERSION", "9.9.9")
	t.Setenv("MIRASIM_SEAL_PUBKEY", "AAAA")
	t.Setenv("UPSTREAM_PROXY", "http://127.0.0.1:7890")
	t.Setenv("CLAUDE_CLOAK_MODE", "strict")
	t.Setenv("CAPACITY_RETRIES", "5")
	t.Setenv("CAPACITY_BACKOFF_MS", "500")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9999 || cfg.Host != "127.0.0.1" || cfg.AdminPassword != "pw" {
		t.Fatalf("env 未生效: %+v", cfg)
	}
	if cfg.RelayURL != "http://relay.local" || cfg.AuthURL != "http://auth.local" {
		t.Fatalf("上游覆盖未生效: %+v", cfg)
	}
	if cfg.ClientVersion != "9.9.9" || cfg.SealPubkey != "AAAA" || cfg.ClaudeCloakMode != "strict" {
		t.Fatalf("env 未生效: %+v", cfg)
	}
	if cfg.UpstreamProxy != "http://127.0.0.1:7890" {
		t.Fatalf("代理未生效: %+v", cfg)
	}
	if cfg.CapacityRetries != 5 || cfg.CapacityBackoffMS != 500 {
		t.Fatalf("重试配置未生效: %+v", cfg)
	}
	// 设了 MASTER_KEY 不应生成 master.key 文件
	if _, err := os.Stat(filepath.Join(dir, "master.key")); !os.IsNotExist(err) {
		t.Fatal("显式 MASTER_KEY 时不应写 master.key")
	}
}

func TestMasterKeyAutoGenerate(t *testing.T) {
	dir := t.TempDir()
	k1, err := LoadOrCreateMasterKey(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != 32 {
		t.Fatalf("应为 32 字节: %d", len(k1))
	}
	path := filepath.Join(dir, "master.key")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("master.key 权限应为 0600: %o", info.Mode().Perm())
	}
	// 再次加载应读出同一把
	k2, err := LoadOrCreateMasterKey(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(k1) != hex.EncodeToString(k2) {
		t.Fatal("重载后 master key 不一致")
	}
}

func TestMasterKeyInvalid(t *testing.T) {
	if _, err := LoadOrCreateMasterKey(t.TempDir(), "not-hex"); err == nil {
		t.Fatal("非 hex 应报错")
	}
	if _, err := LoadOrCreateMasterKey(t.TempDir(), hex.EncodeToString(make([]byte, 16))); err == nil {
		t.Fatal("非 32 字节应报错")
	}
}

func TestLoadInvalidInt(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PORT", "abc")
	if _, err := Load(); err == nil {
		t.Fatal("非法 PORT 应报错")
	}
}
