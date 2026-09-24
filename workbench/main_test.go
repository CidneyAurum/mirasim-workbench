package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecurityBoundary(t *testing.T) {
	a := &app{addr: "127.0.0.1:7901"}
	for _, tc := range []struct {
		path, host, origin, header string
		want                       int
	}{
		{"/identity", "127.0.0.1:7901", "", "", 200},
		{"/", "evil.test:7901", "", "", 403},
		{"/api/reveal", "127.0.0.1:7901", "", "", 403},
		{"/api/reveal", "127.0.0.1:7901", "https://evil.test", "1", 403},
		{"/api/missing", "127.0.0.1:7901", "http://127.0.0.1:7901", "1", 404},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Host = tc.host
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Mir-Workbench", tc.header)
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%+v got %d", tc, w.Code)
		}
	}
	for _, addr := range []string{"0.0.0.0:7901", "evil.test:7901", ":7901"} {
		if validateAddr(addr) == nil {
			t.Errorf("accepted %s", addr)
		}
	}
}
func TestAdminAllowlist(t *testing.T) {
	for _, path := range []string{"../login", "login", "logout", "accounts/../../settings", "settings/anything"} {
		if allowedAdmin("POST", path) {
			t.Fatal(path)
		}
	}
	if !allowedAdmin("PATCH", "keys/abc-123") || !allowedAdmin("POST", "accounts/abc_123/refresh") {
		t.Fatal("valid paths rejected")
	}
}

func TestModuleMIME(t *testing.T) {
	a := &app{addr: "127.0.0.1:7901"}
	r := httptest.NewRequest("GET", "/protocol.mjs", nil)
	r.Host = a.addr
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Fatal("module must have JavaScript MIME type")
	}
}
func TestSettingsValidation(t *testing.T) {
	for _, raw := range []string{`{"capacity_retries":"-1"}`, `{"capacity_retries":"1.5"}`, `{"upstream_proxy":"file:///C:/secret"}`, `{"rate_multiplier":"NaN"}`, `{"model_prices":"{\"m\":{\"input\":-1}}"}`, `{"claude_cloak_mode":"other"}`} {
		if validateSettings([]byte(raw)) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if err := validateSettings([]byte(`{"upstream_proxy":"http://127.0.0.1:7890","capacity_retries":"2","rate_multiplier":"1"}`)); err != nil {
		t.Fatal(err)
	}
}
func TestProtectedCredential(t *testing.T) {
	dir := t.TempDir()
	first, err := loadPassword(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadPassword(dir)
	if err != nil || first != second {
		t.Fatal("credential must survive restart")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "data", "workbench-secret.dpapi"))
	if bytes.Contains(b, []byte(first)) {
		t.Fatal("plaintext credential on disk")
	}
}
func TestTailIsBounded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "test.log")
	_ = os.WriteFile(p, []byte(strings.Repeat("x", 90000)+"end"), 0600)
	if got := readTail(p); len(got) > 64<<10 || !strings.HasSuffix(got, "end") {
		t.Fatal("bad tail")
	}
}

// Integration uses a completely separate temporary deployment, never user data
// and never a real account. Set MIRASIM_TEST_BINARY to enable it.
func TestGatewayIntegration(t *testing.T) {
	bin := os.Getenv("MIRASIM_TEST_BINARY")
	if bin == "" {
		t.Skip("set MIRASIM_TEST_BINARY for real gateway integration")
	}
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "logs"), 0700)
	_ = os.MkdirAll(filepath.Join(dir, "data"), 0700)
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "mirasim2api.exe"), b, 0700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	ln.Close()
	a := &app{repo: dir, addr: "127.0.0.1:7901", gateway: "http://" + address, password: "integration-only", client: &http.Client{Timeout: 10 * time.Second}}
	if err = a.startGateway(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.serviceMu.Lock(); defer a.serviceMu.Unlock(); _ = a.stopLocked() })
	call := func(method, path string, body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/api/"+path, bytes.NewReader(raw))
		r.Host = a.addr
		r.Header.Set("X-Mir-Workbench", "1")
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		var data map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &data)
		return w.Code, data
	}
	if code, data := call("GET", "admin/summary", nil); code != 200 || data["auth_disabled"] != false {
		t.Fatalf("summary %d %+v", code, data)
	}
	if code, _ := call("POST", "admin/accounts/oauth/complete", map[string]string{"callback_url": "http://%invalid"}); code != 400 {
		t.Fatal("malformed callback should be rejected locally")
	}
	code, created := call("POST", "admin/keys", map[string]any{"name": "integration", "concurrency": 2, "rate_limit_rpm": 10})
	if code != 201 {
		t.Fatalf("create %d %+v", code, created)
	}
	key := created["key"].(string)
	id := created["item"].(map[string]any)["id"].(string)
	if code, data := call("GET", "admin/keys", nil); code != 200 {
		t.Fatal(data)
	} else {
		raw, _ := json.Marshal(data)
		if bytes.Contains(raw, []byte(key)) {
			t.Fatal("plaintext key leaked in list")
		}
	}
	if code, data := call("POST", "request", map[string]any{"endpoint": "models", "key": key}); code != 200 {
		t.Fatal(data)
	} else {
		raw, _ := json.Marshal(data)
		if !bytes.Contains(raw, []byte("kimi-k3")) || !bytes.Contains(raw, []byte("deepseek-flash")) {
			t.Fatal("priority models missing")
		}
	}
	if code, data := call("POST", "request", map[string]any{"endpoint": "chat/completions", "key": key, "payload": map[string]any{"model": "kimi-k3", "messages": []map[string]string{{"role": "user", "content": "test"}}}}); code != 503 {
		t.Fatalf("empty pool must fail honestly: %d %+v", code, data)
	}
	if code, _ := call("PUT", "admin/settings", map[string]string{"capacity_retries": "-1"}); code != 400 {
		t.Fatal("invalid settings saved")
	}
	if code, data := call("PUT", "admin/settings", map[string]string{"capacity_retries": "3"}); code != 200 {
		t.Fatal(data)
	}
	if code, data := call("PATCH", "admin/keys/"+id, map[string]bool{"enabled": false}); code != 200 {
		t.Fatal(data)
	}
	if code, _ := call("POST", "request", map[string]any{"endpoint": "models", "key": key}); code != 401 {
		t.Fatal("disabled key accepted")
	}
	if code, data := call("DELETE", "admin/keys/"+id, nil); code != 200 {
		t.Fatal(data)
	}
	if err = a.startGateway(); err != nil {
		t.Fatal("duplicate start", err)
	}
	// Wrong creation time must never terminate the actual gateway.
	rec := a.record()
	rec.Created++
	if ownedProcess(rec, a.binaryPath(), true) {
		t.Fatal("accepted mismatched process")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.gateway+"/health", nil)
	resp, err := a.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if code, data := call("POST", "service/stop", nil); code != 200 {
		t.Fatal(data)
	}
	if a.isOwned() {
		t.Fatal("still owned after stop")
	}
	if code, data := call("POST", "service/start", nil); code != 200 {
		t.Fatal(data)
	}
	if code, data := call("GET", "admin/settings", nil); code != 200 {
		t.Fatal("relogin after restart", data)
	}
}
