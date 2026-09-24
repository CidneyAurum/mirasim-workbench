package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (a *app) adminCookie(ctx context.Context) (*http.Cookie, error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	if a.cookie != nil && time.Now().Before(a.cookieUntil) {
		return a.cookie, nil
	}
	b, _ := json.Marshal(map[string]string{"password": a.password})
	req, _ := http.NewRequestWithContext(ctx, "POST", a.gateway+"/admin/api/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("本地管理会话认证失败，请重启网关")
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "mrs_admin" {
			a.cookie = cookie
			a.cookieUntil = time.Now().Add(7 * time.Hour)
			return cookie, nil
		}
	}
	return nil, errors.New("管理接口没有返回受保护的会话")
}

var itemRoute = regexp.MustCompile(`^(accounts|keys)/[a-zA-Z0-9_-]+$`)
var refreshRoute = regexp.MustCompile(`^accounts/[a-zA-Z0-9_-]+/refresh$`)

func allowedAdmin(method, path string) bool {
	if method == "GET" {
		return path == "summary" || path == "accounts" || path == "keys" || path == "usage/logs" || path == "usage/summary" || path == "settings" || path == "accounts/oauth/providers"
	}
	if method == "PATCH" || method == "DELETE" {
		return itemRoute.MatchString(path)
	}
	if method == "PUT" {
		return path == "settings"
	}
	if method == "POST" {
		return path == "keys" || path == "accounts/oauth/start" || path == "accounts/oauth/complete" || path == "accounts/email/start" || path == "accounts/email/complete" || refreshRoute.MatchString(path)
	}
	return false
}

func (a *app) handleAdmin(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/")
	if !allowedAdmin(r.Method, path) {
		fail(w, 404, errors.New("未开放的管理接口"))
		return
	}
	if !a.isOwned() {
		fail(w, 503, errors.New("网关未启动，请先启动网关"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, 413, err)
		return
	}
	if path == "settings" && r.Method == "PUT" {
		if err := validateSettings(body); err != nil {
			fail(w, 400, err)
			return
		}
	}
	if path == "accounts/oauth/complete" && r.Method == "POST" {
		var form struct {
			CallbackURL string `json:"callback_url"`
		}
		if json.Unmarshal(body, &form) == nil && form.CallbackURL != "" {
			// Upstream's callback parser assumes Parse returned a non-nil URL.
			// Reject malformed URL escapes before forwarding user input.
			if _, parseErr := url.Parse(form.CallbackURL); parseErr != nil {
				fail(w, 400, errors.New("回调链接格式不正确，请完整复制浏览器地址栏"))
				return
			}
		}
	}
	cookie, err := a.adminCookie(r.Context())
	if err != nil {
		fail(w, 502, err)
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), r.Method, a.gateway+"/admin/api/"+path, bytes.NewReader(body))
	req.URL.RawQuery = r.URL.RawQuery
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := a.client.Do(req)
	if err != nil {
		fail(w, 502, errors.New("网关请求失败，请检查运行日志"))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		a.sessionMu.Lock()
		a.cookie = nil
		a.sessionMu.Unlock()
	}
	// Do not forward admin cookies, redirects, or any upstream CORS headers.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 8<<20))
}

func validateSettings(body []byte) error {
	var values map[string]string
	if err := json.Unmarshal(body, &values); err != nil {
		return errors.New("设置须为 JSON 字符串字典")
	}
	for key, value := range values {
		if value == "" {
			continue
		}
		switch key {
		case "upstream_proxy":
			u, err := url.Parse(value)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Fragment != "" {
				return errors.New("代理须为完整的 http:// 或 https:// 地址")
			}
		case "claude_cloak_mode":
			if value != "relaxed" && value != "strict" {
				return errors.New("Claude 模式只能为 relaxed 或 strict")
			}
		case "capacity_retries", "capacity_backoff_ms":
			n, err := strconv.Atoi(value)
			max := 10
			if key == "capacity_backoff_ms" {
				max = 60000
			}
			if err != nil || n < 0 || n > max {
				return fmt.Errorf("%s 须为 0–%d 的整数", key, max)
			}
		case "rate_multiplier":
			n, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
				return errors.New("计费倍率须为非负有限数字")
			}
		case "model_prices":
			var prices map[string]map[string]float64
			if err := json.Unmarshal([]byte(value), &prices); err != nil || prices == nil {
				return errors.New("价格表须为模型名到 input/output/cached 单价的 JSON 对象")
			}
			for _, p := range prices {
				for k, n := range p {
					if (k != "input" && k != "output" && k != "cached") || n < 0 {
						return errors.New("模型单价字段或数值不合法")
					}
				}
			}
		default:
			return fmt.Errorf("未知设置 %s", key)
		}
	}
	return nil
}

func (a *app) handleRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint string          `json:"endpoint"`
		Key      string          `json:"key"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := decode(w, r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	method := "POST"
	switch body.Endpoint {
	case "models":
		method = "GET"
	case "messages", "responses", "chat/completions":
		if len(body.Payload) == 0 || !json.Valid(body.Payload) {
			fail(w, 400, errors.New("缺少 JSON 请求体"))
			return
		}
	default:
		fail(w, 400, errors.New("不支持的测试接口"))
		return
	}
	if strings.TrimSpace(body.Key) == "" {
		fail(w, 400, errors.New("请先填写 API Key"))
		return
	}
	if !a.isOwned() {
		fail(w, 503, errors.New("网关未启动"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, a.gateway+"/v1/"+body.Endpoint, bytes.NewReader(body.Payload))
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(body.Key))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	c := &http.Client{Transport: a.client.Transport, CheckRedirect: a.client.CheckRedirect}
	resp, err := c.Do(req)
	if err != nil {
		fail(w, 502, errors.New("请求失败或已取消，请检查代理设置及网关日志"))
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 4096)
	for {
		n, e := resp.Body.Read(buf)
		if n > 0 {
			if _, we := w.Write(buf[:n]); we != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if e != nil {
			return
		}
	}
}

func (a *app) handleLogs(w http.ResponseWriter, r *http.Request) {
	jsonReply(w, 200, map[string]string{"gateway": readTail(filepath.Join(a.repo, "logs", "server.log")), "workbench": readTail(filepath.Join(a.repo, "logs", "workbench.log"))})
}
func (a *app) handleDesktop(w http.ResponseWriter, r *http.Request) {
	if err := launchDesktop(); err != nil {
		fail(w, 500, err)
		return
	}
	jsonReply(w, 200, map[string]bool{"ok": true})
}
func (a *app) handleReveal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Folder string `json:"folder"`
	}
	if err := decode(w, r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	if body.Folder != "data" && body.Folder != "logs" && body.Folder != "" {
		fail(w, 400, errors.New("非法目录"))
		return
	}
	if err := reveal(filepath.Join(a.repo, body.Folder)); err != nil {
		fail(w, 500, err)
		return
	}
	jsonReply(w, 200, map[string]bool{"ok": true})
}
func (a *app) handleExternal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := decode(w, r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	u, err := url.Parse(body.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Host != "auth.mirasim.ai" && body.URL != "https://github.com/Essaim8/mirasim2api") {
		fail(w, 400, errors.New("只允许打开 Mirasim 认证站点和项目仓库"))
		return
	}
	if err = openExternal(body.URL); err != nil {
		fail(w, 500, err)
		return
	}
	jsonReply(w, 200, map[string]bool{"ok": true})
}
