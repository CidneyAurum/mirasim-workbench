package mirasim

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestClient(t *testing.T, refreshToken, relayURL, authURL string) *Client {
	t.Helper()
	priv, err := GenerateDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(refreshToken, priv, relayURL, authURL, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// 覆盖凭据链：/auth/refresh 轮换回调、/v1/device/session 明文签名头、
// 模型请求密封头与缓存。
func TestClientCredentialChain(t *testing.T) {
	var rotatedTo atomic.Value
	var sawPlainSig atomic.Bool

	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/refresh" {
			http.NotFound(w, r)
			return
		}
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		if req["refresh_token"] == "" {
			t.Error("refresh 请求缺少 refresh_token")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"access-1","refresh_token":"refresh-2","expires_in":3600}`)
	}))
	defer authSrv.Close()

	relaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/device/session":
			if got := r.Header.Get("Authorization"); got != "Bearer access-1" {
				t.Errorf("session 应带 Bearer access token, got %q", got)
			}
			// 明文签名头（仅此端点）
			for _, k := range []string{"x-mirasim-device", "x-mirasim-ts", "x-mirasim-nonce", "x-mirasim-sig", "x-mirasim-client"} {
				if r.Header.Get(k) == "" {
					t.Errorf("session 缺少明文签名头 %s", k)
				}
			}
			sawPlainSig.Store(true)
			var req map[string]string
			json.NewDecoder(r.Body).Decode(&req)
			if req["publicKey"] == "" || req["deviceId"] == "" {
				t.Error("session 请求缺少 publicKey/deviceId")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ticket":"ticket-1","expires_in":900}`)
		case "/v1/models":
			if got := r.Header.Get("Authorization"); got != "Bearer ticket-1" {
				t.Errorf("模型请求应带 Bearer ticket, got %q", got)
			}
			enc := r.Header.Get("x-mirasim-enc")
			if enc == "" {
				t.Error("模型请求缺少 x-mirasim-enc")
			} else if raw, err := base64.RawURLEncoding.DecodeString(enc); err != nil || len(raw) < 60 {
				t.Errorf("x-mirasim-enc 布局非法: err=%v len=%d", err, len(raw))
			}
			// 模型请求不得带明文签名头
			if r.Header.Get("x-mirasim-sig") != "" || r.Header.Get("x-mirasim-device") != "" {
				t.Error("模型请求不应带明文签名头")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer relaySrv.Close()

	c := newTestClient(t, "refresh-1", relaySrv.URL, authSrv.URL)
	c.OnRotate = func(newToken string) { rotatedTo.Store(newToken) }

	ctx := context.Background()
	tok, err := c.AccessToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "access-1" {
		t.Fatalf("access token 不符: %q", tok)
	}
	if rotatedTo.Load() != "refresh-2" {
		t.Fatalf("轮换回调未触发: %v", rotatedTo.Load())
	}
	if c.RefreshToken() != "refresh-2" {
		t.Fatalf("内存 refresh token 未更新: %q", c.RefreshToken())
	}
	// 缓存命中：再次取 access token 不应再请求（服务器只会返回相同值，无法直接区分，
	// 这里改用 ticket 流程顺带验证整体链路）
	ticket, err := c.Ticket(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ticket != "ticket-1" {
		t.Fatalf("ticket 不符: %q", ticket)
	}
	if !sawPlainSig.Load() {
		t.Fatal("session 请求未见明文签名头")
	}

	resp, err := c.DoSignedJSON(ctx, "GET", "/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("模型请求状态码: %d", resp.StatusCode)
	}

	// InvalidateTicket 后应重新申领
	c.InvalidateTicket()
	c.mu.Lock()
	expired := c.ticket == ""
	c.mu.Unlock()
	if !expired {
		t.Fatal("InvalidateTicket 未清空缓存")
	}
}

func TestAccessTokenCache(t *testing.T) {
	var calls atomic.Int64
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"access_token":"a","refresh_token":"r","expires_in":3600}`)
	}))
	defer authSrv.Close()
	c := newTestClient(t, "r0", "http://unused", authSrv.URL)
	ctx := context.Background()
	for range 3 {
		if _, err := c.AccessToken(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("缓存未生效，refresh 调用 %d 次", calls.Load())
	}
}

func TestAccessTokenHTTPError(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"invalid refresh token"}`)
	}))
	defer authSrv.Close()
	c := newTestClient(t, "r0", "http://unused", authSrv.URL)
	_, err := c.AccessToken(context.Background())
	var httpErr *HTTPError
	if err == nil || !errors.As(err, &httpErr) || httpErr.StatusCode != 401 {
		t.Fatalf("应返回 401 HTTPError: %v", err)
	}
	if !strings.Contains(httpErr.Body, "invalid refresh token") {
		t.Fatalf("HTTPError 应带响应体: %q", httpErr.Body)
	}
}

func TestNewClientProxy(t *testing.T) {
	priv, _ := GenerateDeviceKey()
	if _, err := NewClient("r", priv, "", "", "", "", "://bad-proxy"); err == nil {
		t.Fatal("非法代理地址应报错")
	}
	c, err := NewClient("r", priv, "", "", "", "", "http://127.0.0.1:7890")
	if err != nil {
		t.Fatal(err)
	}
	if c.RelayURL != DefaultRelayURL || c.AuthURL != DefaultAuthURL ||
		c.ClientVersion != DefaultClientVersion || c.SealPubkey != DefaultSealPubkey {
		t.Fatal("默认值未生效")
	}
}

func TestSignedRequestBodyAndHeaders(t *testing.T) {
	var relayRelayed atomic.Bool
	var authSrv *httptest.Server
	var relaySrv *httptest.Server
	authSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"a","refresh_token":"r","expires_in":3600}`)
	}))
	relaySrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/device/session" {
			fmt.Fprint(w, `{"ticket":"t","expires_in":900}`)
			return
		}
		relayRelayed.Store(true)
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"model":"m"}` {
			t.Errorf("body 未透传: %q", body)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type: %q", ct)
		}
		if r.Header.Get("x-mirasim-client") != DefaultClientVersion {
			t.Errorf("x-mirasim-client: %q", r.Header.Get("x-mirasim-client"))
		}
		fmt.Fprint(w, `{}`)
	}))
	defer authSrv.Close()
	defer relaySrv.Close()

	c := newTestClient(t, "r0", relaySrv.URL, authSrv.URL)
	resp, err := c.DoSignedJSON(context.Background(), "POST", "/v1/messages", []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !relayRelayed.Load() {
		t.Fatal("请求未到 relay")
	}
}
