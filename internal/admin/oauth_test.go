package admin

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func fixtureRefreshToken(email string) string {
	claims, _ := json.Marshal(map[string]any{"email": email, "token_type": "refresh", "exp": time.Now().Add(time.Hour).Unix()})
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture-signature"
}

// Exercise a real TCP callback, not a ResponseRecorder: Server.Close inside
// the request handler used to destroy the socket before HTML reached Edge.
func TestOAuthCallbackDeliversSuccessHTML(t *testing.T) {
	srv, _ := newTestServer(t)
	code, flow := doJSON(t, "POST", srv.URL+"/admin/api/accounts/oauth/start", `{"provider":"github","mode":"auto"}`)
	if code != 200 {
		t.Fatalf("start status = %d", code)
	}
	callback := flow["redirect_uri"].(string)
	query := url.Values{"access_token": {"unused-fixture-access"}, "refresh_token": {fixtureRefreshToken("callback@example.invalid")}, "state": {"upstream-rewritten-state"}}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(callback + "?" + query.Encode())
	if err != nil {
		t.Fatal("callback connection closed before delivering the success page")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || !strings.Contains(string(body), "登录成功") {
		t.Fatal("success page not delivered completely")
	}
	_, progress := doJSON(t, "POST", srv.URL+"/admin/api/accounts/oauth/complete", `{"flow_id":"`+flow["flow_id"].(string)+`"}`)
	if progress["status"] != "done" {
		t.Fatal("callback did not finish account import")
	}
	// Edge retries or a user refresh must keep showing the same result, without
	// importing another account or replacing the already-accepted token.
	for i := 0; i < 3; i++ {
		r, err := client.Get(callback)
		if err != nil {
			t.Fatal("completed callback listener closed too soon")
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if !strings.Contains(string(b), "<h1 id=\"status\">登录成功</h1>") {
			t.Fatal("refresh lost the completion result")
		}
	}
	_, list := doJSON(t, "GET", srv.URL+"/admin/api/accounts", "")
	if len(list["items"].([]any)) != 1 {
		t.Fatal("duplicate callback imported duplicate accounts")
	}
	if strings.Contains(string(body), query.Get("refresh_token")) || strings.Contains(string(body), query.Get("access_token")) {
		t.Fatal("HTML leaked credentials")
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing credential protection headers")
	}
}

func TestOAuthFragmentProbeDoesNotConsumeFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	_, flow := doJSON(t, "POST", srv.URL+"/admin/api/accounts/oauth/start", `{"provider":"google","mode":"auto"}`)
	callback, id := flow["redirect_uri"].(string), flow["flow_id"].(string)
	client := &http.Client{Timeout: 3 * time.Second}
	// #fragments do not travel in HTTP. The first request is just /callback.
	r, err := client.Get(callback)
	if err != nil {
		t.Fatal("tokenless callback probe closed the listener")
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(b), "history.replaceState") || !strings.Contains(string(b), "location.hash") {
		t.Fatal("fragment bridge or URL cleanup missing")
	}
	_, progress := doJSON(t, "POST", srv.URL+"/admin/api/accounts/oauth/complete", `{"flow_id":"`+id+`"}`)
	if progress["status"] != "pending" {
		t.Fatal("probe consumed pending OAuth flow")
	}
	post, _ := json.Marshal(map[string]string{"refresh_token": fixtureRefreshToken("fragment@example.invalid")})
	req, _ := http.NewRequest("POST", callback, strings.NewReader(string(post)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mirasim-Callback", id)
	u, _ := url.Parse(callback)
	req.Header.Set("Origin", "http://"+u.Host)
	r, err = client.Do(req)
	if err != nil {
		t.Fatal("fragment callback failed")
	}
	defer r.Body.Close()
	var result map[string]any
	_ = json.NewDecoder(r.Body).Decode(&result)
	if r.StatusCode != 200 || result["status"] != "done" {
		t.Fatal("fragment callback did not import account")
	}
	_, progress = doJSON(t, "POST", srv.URL+"/admin/api/accounts/oauth/complete", `{"flow_id":"`+id+`"}`)
	if progress["status"] != "done" {
		t.Fatal("fragment callback did not complete polling state")
	}
}

func TestOAuthCallbackRejectsForeignOrigin(t *testing.T) {
	srv, _ := newTestServer(t)
	_, flow := doJSON(t, "POST", srv.URL+"/admin/api/accounts/oauth/start", `{"provider":"github"}`)
	client := &http.Client{Timeout: 3 * time.Second}
	for _, tc := range []struct{ origin, header, host string }{
		{"https://evil.example", flow["flow_id"].(string), ""},
		{"", "wrong-flow", ""},
		{"", flow["flow_id"].(string), "evil.example"},
	} {
		req, _ := http.NewRequest("POST", flow["redirect_uri"].(string), strings.NewReader(`{"refresh_token":"ignored"}`))
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("X-Mirasim-Callback", tc.header)
		if tc.host != "" {
			req.Host = tc.host
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatal("callback listener stopped")
		}
		r.Body.Close()
		if r.StatusCode != 403 {
			t.Fatalf("foreign callback accepted: %d", r.StatusCode)
		}
	}
}

func TestOAuthCallbackInvalidTokenShowsErrorPage(t *testing.T) {
	srv, _ := newTestServer(t)
	_, flow := doJSON(t, "POST", srv.URL+"/admin/api/accounts/oauth/start", `{"provider":"github"}`)
	client := &http.Client{Timeout: 3 * time.Second}
	r, err := client.Get(flow["redirect_uri"].(string) + "?refresh_token=invalid")
	if err != nil {
		t.Fatal("invalid token should render an error, not break the socket")
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(body), "<h1 id=\"status\">登录失败</h1>") {
		t.Fatal("missing clear error page")
	}
	_, p := doJSON(t, "POST", srv.URL+"/admin/api/accounts/oauth/complete", `{"flow_id":"`+flow["flow_id"].(string)+`"}`)
	if p["status"] != "error" {
		t.Fatal("polling did not expose the failure")
	}
}

func TestExtractRefreshTokenRecovery(t *testing.T) {
	for _, value := range []string{
		"http://127.0.0.1:1234/callback?access_token=access&refresh_token=refresh",
		"http://127.0.0.1:1234/callback#access_token=access&refresh_token=refresh",
		"refresh_token=refresh&access_token=access", "?refresh_token=refresh",
	} {
		got, err := extractRefreshToken(value)
		if err != nil || got != "refresh" {
			t.Fatal("valid callback recovery failed")
		}
	}
	for _, value := range []string{"http://%invalid", "http://127.0.0.1/callback?access_token=access", ""} {
		if _, err := extractRefreshToken(value); err == nil {
			t.Fatal("invalid callback accepted")
		}
	}
}

func TestOAuthFlowExpiry(t *testing.T) {
	fs := &flowStore{flows: map[string]*oauthFlow{}}
	fs.put(&oauthFlow{id: "expired", expiresAt: time.Now().Add(-time.Second)})
	if fs.lookup("expired") != nil {
		t.Fatal("expired callback accessible")
	}
	if _, found := fs.get("expired"); found {
		t.Fatal("expired polling accessible")
	}
}

// Optional browser verification fixture. All tokens/accounts here are synthetic
// and stored in t.TempDir; this handler does not start upstream quota polling.
func TestOAuthBrowserFixture(t *testing.T) {
	if os.Getenv("MIRASIM_OAUTH_BROWSER_FIXTURE") != "1" {
		t.Skip("opt-in browser fixture")
	}
	srv, _ := newTestServer(t)
	_, flow := doJSON(t, "POST", srv.URL+"/admin/api/accounts/oauth/start", `{"provider":"google","mode":"auto"}`)
	callback := flow["redirect_uri"].(string)
	fmt.Printf("BROWSER_FIXTURE %s#refresh_token=%s\n", callback, fixtureRefreshToken("browser-fixture@example.invalid"))
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		_, p := doJSON(t, "POST", srv.URL+"/admin/api/accounts/oauth/complete", `{"flow_id":"`+flow["flow_id"].(string)+`"}`)
		if p["status"] == "done" {
			fmt.Println("BROWSER_FIXTURE_DONE")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("browser fixture was not completed")
}
