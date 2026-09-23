package admin

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mirasim2api/internal/config"
	"mirasim2api/internal/mirasim"
	"mirasim2api/internal/pool"
	"mirasim2api/internal/runtimecfg"
	"mirasim2api/internal/store"
)

// newTestServer 起一个免密（AdminPassword 空）的管理端测试服务器。
func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	st, err := store.Open(t.TempDir(), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	pl := pool.New(st, func(a *store.Account) (*mirasim.Client, error) {
		_, priv, _ := ed25519.GenerateKey(nil)
		return mirasim.NewClientFunc(a.RefreshToken, priv, "", "", "", "", nil)
	})
	settings := runtimecfg.New(st, nil, map[string]string{
		runtimecfg.KeyCloakMode: "relaxed",
	})
	h := NewHandler(Deps{
		Store:    st,
		Pool:     pl,
		Config:   &config.Config{AdminPassword: ""},
		Settings: settings,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, st
}

func doJSON(t *testing.T, method, url string, body string) (int, map[string]any) {
	t.Helper()
	var rdr *strings.Reader = strings.NewReader(body)
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestKeyCreateAndValidate 锁住 validate() 返回值语义：
// 成功创建须 201 且返回一次性明文；非法并发须 400 且给出中文错误消息。
func TestKeyCreateAndValidate(t *testing.T) {
	srv, _ := newTestServer(t)

	// 合法创建
	code, res := doJSON(t, "POST", srv.URL+"/admin/api/keys", `{"name":"smoke","concurrency":3}`)
	if code != http.StatusCreated {
		t.Fatalf("创建 key 应 201，实得 %d: %v", code, res)
	}
	pt, _ := res["key"].(string)
	if !strings.HasPrefix(pt, "sk-") {
		t.Fatalf("明文 key 应以 sk- 开头: %v", res)
	}
	if res["warning"] == nil {
		t.Fatalf("应含一次性提示: %v", res)
	}
	item, _ := res["item"].(map[string]any)
	if item["key_display"] == pt {
		t.Fatalf("展示形式不应等于明文: %v", item)
	}

	// 非法并发 → 400 + 非空消息
	code, res = doJSON(t, "POST", srv.URL+"/admin/api/keys", `{"name":"x","concurrency":99999}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法并发应 400，实得 %d: %v", code, res)
	}
	if msg, _ := res["error"].(string); msg == "" {
		t.Fatalf("400 时应给出错误消息: %v", res)
	}
}

// TestSettingsRoundTrip 设置写入后读回应为 database 来源。
func TestSettingsRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)

	code, res := doJSON(t, "PUT", srv.URL+"/admin/api/settings",
		`{"claude_cloak_mode":"strict"}`)
	if code != http.StatusOK {
		t.Fatalf("写设置应 200，实得 %d: %v", code, res)
	}

	code, res = doJSON(t, "GET", srv.URL+"/admin/api/settings", ``)
	if code != http.StatusOK {
		t.Fatalf("读设置应 200，实得 %d", code)
	}
	items, _ := res["settings"].([]any)
	found := false
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["key"] == runtimecfg.KeyCloakMode {
			found = true
			if m["value"] != "strict" || m["source"] != runtimecfg.SourceDatabase {
				t.Fatalf("cloak_mode 应为 strict/database: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("设置列表应包含 claude_cloak_mode: %v", items)
	}

	// 非法键 → 400
	code, _ = doJSON(t, "PUT", srv.URL+"/admin/api/settings", `{"no_such_key":"1"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法设置键应 400，实得 %d", code)
	}
}

// TestSessionAuthRequired 设了密码后，未登录访问受保护接口应 401。
func TestSessionAuthRequired(t *testing.T) {
	key := make([]byte, 32)
	st, err := store.Open(t.TempDir(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pl := pool.New(st, func(a *store.Account) (*mirasim.Client, error) { return nil, nil })
	settings := runtimecfg.New(st, nil, nil)
	h := NewHandler(Deps{
		Store: st, Pool: pl,
		Config:   &config.Config{AdminPassword: "pw123"},
		Settings: settings,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	code, _ := doJSON(t, "GET", srv.URL+"/admin/api/keys", ``)
	if code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，实得 %d", code)
	}
	// 错误密码
	code, _ = doJSON(t, "POST", srv.URL+"/admin/api/login", `{"password":"bad"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401，实得 %d", code)
	}
}
