package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// oauthFlowTTL 是 OAuth 登录流程的有效期（服务端签发的 state 只有 10 分钟）。
const oauthFlowTTL = 10 * time.Minute

// 允许的 OAuth 提供商（PROTOCOL.md：github / google）。
var oauthProviders = map[string]bool{"github": true, "google": true}

// flowResult 是一次 OAuth 流程的最终结果。
type flowResult struct {
	done    bool
	errMsg  string
	account *accountBrief
}

type accountBrief struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// flowStore 管理进行中的 OAuth 流程（auto 模式持有 loopback 监听器）。
type flowStore struct {
	mu    sync.Mutex
	flows map[string]*oauthFlow
}

type oauthFlow struct {
	id        string
	provider  string
	mode      string // "auto"（loopback 独占端口）/ "manual"（粘贴回调链接）
	authURL   string
	expiresAt time.Time
	listener  net.Listener // 仅 auto
	srv       *http.Server
	result    *flowResult
}

func newFlowStore() *flowStore {
	fs := &flowStore{flows: map[string]*oauthFlow{}}
	go fs.sweeper()
	return fs
}

// sweeper 定期清理过期流程（含关闭遗留监听器）。
func (fs *flowStore) sweeper() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		fs.mu.Lock()
		for id, f := range fs.flows {
			if time.Now().After(f.expiresAt) {
				f.close()
				delete(fs.flows, id)
			}
		}
		fs.mu.Unlock()
	}
}

func (f *oauthFlow) close() {
	if f.srv != nil {
		_ = f.srv.Close()
	} else if f.listener != nil {
		_ = f.listener.Close()
	}
}

func (fs *flowStore) put(f *oauthFlow) {
	fs.mu.Lock()
	fs.flows[f.id] = f
	fs.mu.Unlock()
}

// finish 标记流程完成并释放监听器（auto 模式回调成功后调用）。
func (fs *flowStore) finish(id string, res *flowResult) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if f, ok := fs.flows[id]; ok {
		f.result = res
		f.close()
	}
}

// get 取流程结果：found=false 表示不存在或已过期。pending 时 result=nil。
func (fs *flowStore) get(id string) (res *flowResult, found bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	f, ok := fs.flows[id]
	if !ok || time.Now().After(f.expiresAt) {
		return nil, false
	}
	if f.result == nil {
		return nil, true
	}
	return f.result, true
}

// handleOAuthProviders 透传认证后端的提供商清单。
func (h *Handler) handleOAuthProviders(w http.ResponseWriter, r *http.Request) {
	resp, err := h.http.Get(strings.TrimRight(h.cfg.AuthURL, "/") + "/auth/oauth/providers")
	if err != nil {
		writeErr(w, http.StatusBadGateway, "无法连接认证后端: "+err.Error())
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAdminBody))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "读取认证后端响应失败")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// handleOAuthStart 发起 OAuth：{"provider":"github|google", "mode":"auto|manual"}。
// auto 模式监听 127.0.0.1 独占临时端口认领回调（服务端会换掉 state，
// 只有独占端口能把回调归到本流程）；manual 模式让用户事后粘贴回调链接。
func (h *Handler) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		Mode     string `json:"mode"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	body.Provider = strings.ToLower(strings.TrimSpace(body.Provider))
	if !oauthProviders[body.Provider] {
		writeErr(w, http.StatusBadRequest, "不支持的登录提供商（支持 github / google）")
		return
	}
	if body.Mode == "" {
		body.Mode = "auto"
	}
	if body.Mode != "auto" && body.Mode != "manual" {
		writeErr(w, http.StatusBadRequest, "mode 只能是 auto 或 manual")
		return
	}

	state := randHex(16) // 本地流程 ID（服务端会换掉它，仅用于本地认领）
	var redirectURI string
	var listener net.Listener
	if body.Mode == "auto" {
		var err error
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "无法监听本地回调端口: "+err.Error())
			return
		}
		redirectURI = fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	} else {
		// 手动模式：redirect_uri 指到一个无监听端口，用户从浏览器地址栏复制完整链接
		redirectURI = "http://127.0.0.1:9/callback"
	}
	authURL := fmt.Sprintf("%s/auth/oauth/%s/login?redirect_uri=%s&state=%s",
		strings.TrimRight(h.cfg.AuthURL, "/"), body.Provider, url.QueryEscape(redirectURI), state)

	flow := &oauthFlow{
		id:        state,
		provider:  body.Provider,
		mode:      body.Mode,
		authURL:   authURL,
		expiresAt: time.Now().Add(oauthFlowTTL),
		listener:  listener,
	}
	if listener != nil {
		mux := http.NewServeMux()
		mux.HandleFunc("/callback", h.serveOAuthCallback(flow.id))
		flow.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = flow.srv.Serve(listener) }()
	}
	h.flows.put(flow)

	writeOK(w, map[string]any{
		"flow_id":      flow.id,
		"state":        state, // 与 flow_id 相同，供兼容旧前端
		"mode":         body.Mode,
		"provider":     body.Provider,
		"auth_url":     authURL,
		"redirect_uri": redirectURI,
		"expires_at":   flow.expiresAt.Unix(),
	})
}

// serveOAuthCallback 处理 loopback 回调：取 refresh_token 参数验收入池。
func (h *Handler) serveOAuthCallback(flowID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("refresh_token")
		var res *flowResult
		if token == "" {
			res = &flowResult{done: true, errMsg: "回调链接缺少 refresh_token 参数"}
		} else if acc, info, err := h.addAccount(token, h.flowProvider(flowID), ""); err != nil {
			res = &flowResult{done: true, errMsg: err.Error()}
		} else {
			res = &flowResult{done: true, account: &accountBrief{ID: acc.ID, Email: acc.Email, Name: acc.Name, Provider: acc.Provider}}
			h.logger.Info("oauth account added", "email", acc.Email, "provider", acc.Provider, "plan", info.Plan)
		}
		h.flows.finish(flowID, res)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.errMsg != "" {
			fmt.Fprintf(w, "<html><body style='font-family:sans-serif;padding:2em'><h2>登录失败</h2><p>%s</p><p>请回到管理后台重新发起。</p></body></html>", htmlEscape(res.errMsg))
			return
		}
		fmt.Fprint(w, "<html><body style='font-family:sans-serif;padding:2em'><h2>登录成功</h2><p>账号已加入账号池，可以关闭本页并回到管理后台。</p></body></html>")
	}
}

// flowProvider 取流程发起时的 provider（回调时复用）。
func (h *Handler) flowProvider(flowID string) string {
	h.flows.mu.Lock()
	defer h.flows.mu.Unlock()
	if f, ok := h.flows.flows[flowID]; ok {
		return f.provider
	}
	return ""
}

// handleOAuthComplete 完成 OAuth 流程。三种形态：
//  1. {"token": "<refresh_token>"} —— 粘贴裸 refresh token 直接入池；
//  2. {"callback_url": "http://127.0.0.1:.../callback?...refresh_token=..."} —— 粘贴回调链接；
//  3. {"flow_id"/"state": "..."} —— 查询 auto 流程进度（pending / done / error）。
func (h *Handler) handleOAuthComplete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FlowID      string `json:"flow_id"`
		State       string `json:"state"`
		CallbackURL string `json:"callback_url"`
		Token       string `json:"token"`
		Provider    string `json:"provider"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}

	switch {
	case strings.TrimSpace(body.Token) != "":
		h.finishWithToken(w, strings.TrimSpace(body.Token), body.Provider)

	case strings.TrimSpace(body.CallbackURL) != "":
		token, err := extractRefreshToken(body.CallbackURL)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		h.finishWithToken(w, token, body.Provider)

	default:
		flowID := firstNonEmpty(body.FlowID, body.State)
		if flowID == "" {
			writeErr(w, http.StatusBadRequest, "缺少 token、callback_url 或 flow_id")
			return
		}
		res, found := h.flows.get(flowID)
		if !found {
			writeErr(w, http.StatusGone, "登录已过期，请重新发起")
			return
		}
		if res == nil {
			writeOK(w, map[string]any{"status": "pending", "message": "等待浏览器完成授权…"})
			return
		}
		if res.errMsg != "" {
			writeOK(w, map[string]any{"status": "error", "error": res.errMsg})
			return
		}
		writeOK(w, map[string]any{"status": "done", "account": res.account})
	}
}

// finishWithToken 用粘贴的 refresh token 完成入池。
func (h *Handler) finishWithToken(w http.ResponseWriter, token, provider string) {
	acc, _, err := h.addAccount(token, strings.ToLower(strings.TrimSpace(provider)), "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.logger.Info("account added via pasted token", "email", acc.Email, "provider", acc.Provider)
	writeOK(w, map[string]any{
		"status":  "done",
		"account": &accountBrief{ID: acc.ID, Email: acc.Email, Name: acc.Name, Provider: acc.Provider},
	})
}

// extractRefreshToken 从粘贴的回调链接中解析 refresh_token 参数（query 或 fragment）。
func extractRefreshToken(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// 粘贴的可能只是 "?refresh_token=..." 或 "refresh_token=..." 参数串
		if q := u.Query().Get("refresh_token"); q != "" {
			return q, nil
		}
		if strings.Contains(raw, "refresh_token=") {
			vals, perr := url.ParseQuery(strings.TrimLeft(raw, "?#"))
			if perr == nil {
				if q := vals.Get("refresh_token"); q != "" {
					return q, nil
				}
			}
		}
		return "", errors.New("无法解析回调链接，请完整复制浏览器地址栏内容")
	}
	if q := u.Query().Get("refresh_token"); q != "" {
		return q, nil
	}
	if frag := u.Fragment; strings.Contains(frag, "refresh_token=") {
		if vals, perr := url.ParseQuery(frag); perr == nil {
			if q := vals.Get("refresh_token"); q != "" {
				return q, nil
			}
		}
	}
	return "", errors.New("回调链接里没有 refresh_token 参数：请复制浏览器跳转后的完整地址（页面打不开也没关系，从地址栏复制即可）")
}

// handleEmailStart 发送邮箱验证码：{"email": "..."} → POST {auth}/auth/code。
func (h *Handler) handleEmailStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email == "" || !strings.Contains(body.Email, "@") {
		writeErr(w, http.StatusBadRequest, "邮箱地址不合法")
		return
	}
	respBody, status, err := h.postAuthJSON("/auth/code", map[string]string{"email": body.Email})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "发送验证码失败: "+err.Error())
		return
	}
	if status != http.StatusOK {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("发送验证码失败（认证后端 HTTP %d）: %s", status, truncate(string(respBody), 200)))
		return
	}
	out := map[string]any{"ok": true, "email": body.Email}
	// 开发环境认证后端可能直接返回 dev_code
	var parsed map[string]any
	if json.Unmarshal(respBody, &parsed) == nil {
		if dc, _ := parsed["dev_code"].(string); dc != "" {
			out["dev_code"] = dc
		}
	}
	writeOK(w, out)
}

// handleEmailComplete ：{"email", "code"} → POST {auth}/auth/verify → 入池。
func (h *Handler) handleEmailComplete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	body.Code = strings.TrimSpace(body.Code)
	if body.Email == "" || body.Code == "" {
		writeErr(w, http.StatusBadRequest, "缺少 email 或 code")
		return
	}
	respBody, status, err := h.postAuthJSON("/auth/verify", map[string]string{"email": body.Email, "code": body.Code})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "验证码校验失败: "+err.Error())
		return
	}
	if status != http.StatusOK {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("验证码错误或已过期（认证后端 HTTP %d）", status))
		return
	}
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(respBody, &tokens); err != nil || tokens.RefreshToken == "" {
		writeErr(w, http.StatusBadGateway, "认证后端响应缺少 refresh_token")
		return
	}
	acc, _, err := h.addAccount(tokens.RefreshToken, "email", body.Email)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.logger.Info("account added via email code", "email", acc.Email)
	writeOK(w, map[string]any{
		"status":  "done",
		"account": &accountBrief{ID: acc.ID, Email: acc.Email, Name: acc.Name, Provider: acc.Provider},
	})
}

// postAuthJSON 向认证后端发 JSON POST，返回响应体与状态码。
func (h *Handler) postAuthJSON(path string, payload any) ([]byte, int, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	resp, err := h.http.Post(strings.TrimRight(h.cfg.AuthURL, "/")+path, "application/json", strings.NewReader(string(b)))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAdminBody))
	if err != nil {
		return nil, 0, err
	}
	return body, resp.StatusCode, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
