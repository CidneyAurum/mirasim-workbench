package mirasim

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	DefaultRelayURL      = "https://relay.mirasim.ai"
	DefaultAuthURL       = "https://auth.mirasim.ai"
	DefaultClientVersion = "0.0.322"
	// DefaultSealPubkey 是 relay 的 X25519 接收公钥（base64）。
	DefaultSealPubkey = "HlyNMMeGXryasYLJuYQ/9ksCD4AYVVy1zXKAtJdpJn4="

	accessTokenEarlyRefresh = 120 * time.Second // access token 提前 120s 刷新
	ticketEarlyRefresh      = 60 * time.Second  // ticket 提前 60s 重领
)

func nowMillis() int64 { return time.Now().UnixMilli() }

// Client 持有一个账号的凭据链：refresh token → access token → device ticket。
// 设备身份在同一进程的所有账号间共享。
type Client struct {
	RelayURL      string
	AuthURL       string
	ClientVersion string
	SealPubkey    string

	// OnRotate 在 refresh token 轮换后回调（由 pool 注入以持久化）。
	OnRotate func(newRefreshToken string)

	deviceID     string
	devicePubB64 string
	devicePriv   ed25519.PrivateKey

	http *http.Client

	mu           sync.Mutex
	refreshToken string
	accessToken  string
	accessExp    time.Time
	ticket       string
	ticketExp    time.Time
}

// NewClient 创建账号客户端。proxy 为空串则直连。
func NewClient(refreshToken string, devicePriv ed25519.PrivateKey, relayURL, authURL, clientVersion, sealPubkey, proxy string) (*Client, error) {
	if proxy != "" {
		if _, err := url.Parse(proxy); err != nil {
			return nil, fmt.Errorf("mirasim: 代理地址非法: %w", err)
		}
	}
	return NewClientFunc(refreshToken, devicePriv, relayURL, authURL, clientVersion, sealPubkey, func() string { return proxy })
}

// NewClientFunc 同 NewClient，但代理由 proxyFunc 在每次请求时动态求值
// （运行时设置热生效；返回空串表示直连，解析失败的代理地址按直连处理）。
func NewClientFunc(refreshToken string, devicePriv ed25519.PrivateKey, relayURL, authURL, clientVersion, sealPubkey string, proxyFunc func() string) (*Client, error) {
	if relayURL == "" {
		relayURL = DefaultRelayURL
	}
	if authURL == "" {
		authURL = DefaultAuthURL
	}
	if clientVersion == "" {
		clientVersion = DefaultClientVersion
	}
	if sealPubkey == "" {
		sealPubkey = DefaultSealPubkey
	}
	transport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			if proxyFunc == nil {
				return nil, nil
			}
			raw := proxyFunc()
			if raw == "" {
				return nil, nil
			}
			proxyURL, err := url.Parse(raw)
			if err != nil {
				return nil, nil
			}
			return proxyURL, nil
		},
	}
	deviceID, pubB64 := DeviceIdentity(devicePriv)
	return &Client{
		RelayURL:      relayURL,
		AuthURL:       authURL,
		ClientVersion: clientVersion,
		SealPubkey:    sealPubkey,
		deviceID:      deviceID,
		devicePubB64:  pubB64,
		devicePriv:    devicePriv,
		refreshToken:  refreshToken,
		http:          &http.Client{Transport: transport, Timeout: 60 * time.Second},
	}, nil
}

// DeviceID 返回共享设备身份 ID。
func (c *Client) DeviceID() string { return c.deviceID }

// RefreshToken 返回当前 refresh token（轮换后会变）。
func (c *Client) RefreshToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshToken
}

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // 秒；缺省按 1 小时
}

// AccessToken 返回可用的 access token，缓存至过期前 120s。
// 响应里的新 refresh_token 若变化，更新内存并回调 OnRotate。
func (c *Client) AccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.accessToken != "" && time.Now().Before(c.accessExp.Add(-accessTokenEarlyRefresh)) {
		tok := c.accessToken
		c.mu.Unlock()
		return tok, nil
	}
	refreshTok := c.refreshToken
	c.mu.Unlock()

	body, _ := json.Marshal(map[string]string{"refresh_token": refreshTok})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.AuthURL+"/auth/refresh", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("mirasim: 刷新 access token 失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", &HTTPError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	var rr refreshResponse
	if err := json.Unmarshal(respBody, &rr); err != nil {
		return "", fmt.Errorf("mirasim: 刷新响应解析失败: %w", err)
	}
	expiresIn := rr.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	c.mu.Lock()
	c.accessToken = rr.AccessToken
	c.accessExp = time.Now().Add(time.Duration(expiresIn) * time.Second)
	rotated := rr.RefreshToken != "" && rr.RefreshToken != c.refreshToken
	if rotated {
		c.refreshToken = rr.RefreshToken
	}
	onRotate := c.OnRotate
	c.mu.Unlock()

	if rotated && onRotate != nil {
		onRotate(rr.RefreshToken)
	}
	return rr.AccessToken, nil
}

type sessionResponse struct {
	Ticket    string `json:"ticket"`
	ExpiresIn int64  `json:"expires_in"` // 秒；缺省按 900
}

// Ticket 返回 device ticket，缓存至过期前 60s。
// 申领走 POST {relay}/v1/device/session，Bearer access token + 明文签名头。
func (c *Client) Ticket(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.ticket != "" && time.Now().Before(c.ticketExp.Add(-ticketEarlyRefresh)) {
		t := c.ticket
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()

	accessTok, err := c.AccessToken(ctx)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{
		"publicKey": c.devicePubB64,
		"deviceId":  c.deviceID,
	})
	path := "/v1/device/session"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.RelayURL+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessTok)
	// 明文签名头（credential = access token），仅此端点这样发
	for k, v := range SignHeaders(c.devicePriv, c.deviceID, SignParams{
		Method:        http.MethodPost,
		Path:          path,
		Credential:    accessTok,
		ClientVersion: c.ClientVersion,
		Body:          body,
	}) {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("mirasim: 申领 device ticket 失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", &HTTPError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	var sr sessionResponse
	if err := json.Unmarshal(respBody, &sr); err != nil {
		return "", fmt.Errorf("mirasim: session 响应解析失败: %w", err)
	}
	expiresIn := sr.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 900
	}
	c.mu.Lock()
	c.ticket = sr.Ticket
	c.ticketExp = time.Now().Add(time.Duration(expiresIn) * time.Second)
	c.mu.Unlock()
	return sr.Ticket, nil
}

// InvalidateTicket 使缓存的 ticket 失效（如 relay 返回 401 时）。
func (c *Client) InvalidateTicket() {
	c.mu.Lock()
	c.ticket = ""
	c.ticketExp = time.Time{}
	c.mu.Unlock()
}

// HTTPError 带 HTTP 状态码的错误，供 pool 判定是否熔断。
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("mirasim: HTTP %d: %s", e.StatusCode, e.Body)
}

// SignedRequest 构造模型请求：Bearer ticket + x-mirasim-client +
// x-mirasim-enc（密封 4 个签名头，credential = ticket）。
func (c *Client) SignedRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	ticket, err := c.Ticket(ctx)
	if err != nil {
		return nil, err
	}
	signHdrs := SignHeaders(c.devicePriv, c.deviceID, SignParams{
		Method:        method,
		Path:          path,
		Credential:    ticket,
		ClientVersion: c.ClientVersion,
		Body:          body,
	})
	enc, err := SealHeaders(c.SealPubkey, method, path, signHdrs)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.RelayURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+ticket)
	req.Header.Set("x-mirasim-client", c.ClientVersion)
	req.Header.Set("x-mirasim-enc", enc)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// DoSignedJSON 发送签名请求并返回响应（调用方负责关闭 Body）。
func (c *Client) DoSignedJSON(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := c.SignedRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}
