package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// sessionCookie 是管理会话 Cookie 名（HttpOnly）。
	sessionCookie = "mrs_admin"
	// sessionTTL 是会话有效期（8 小时，滑动不刷新）。
	sessionTTL = 8 * time.Hour
)

// deriveSessionKey 由 MASTER_KEY 派生会话签名密钥（HMAC-SHA256(master, info)）。
func deriveSessionKey(masterKey []byte) []byte {
	mac := hmac.New(sha256.New, masterKey)
	mac.Write([]byte("mrs-admin-session-v1"))
	return mac.Sum(nil)
}

// newSessionToken 生成 "v1.{expUnix}.{nonce}.{hmacHex}" 形式的签名会话令牌。
func (h *Handler) newSessionToken(exp time.Time) string {
	payload := fmt.Sprintf("v1.%d.%s", exp.Unix(), randHex(16))
	mac := hmac.New(sha256.New, h.sessionKey)
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

// verifySessionToken 校验令牌签名与有效期。
func (h *Handler) verifySessionToken(token string) bool {
	i := strings.LastIndex(token, ".")
	if i <= 0 {
		return false
	}
	payload, sigHex := token[:i], token[i+1:]
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, h.sessionKey)
	mac.Write([]byte(payload))
	if subtle.ConstantTimeCompare(sig, mac.Sum(nil)) != 1 {
		return false
	}
	parts := strings.SplitN(payload, ".", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < exp
}

func (h *Handler) validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return h.verifySessionToken(c.Value)
}

func (h *Handler) setSessionCookie(w http.ResponseWriter) {
	tok := h.newSessionToken(time.Now().Add(sessionTTL))
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// handleLogin 校验管理密码（常量时间比较），成功后下发会话 Cookie。
// 免密模式（未设 ADMIN_PASSWORD）返回 auth_disabled=true，不下发 Cookie。
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if h.password() == "" {
		writeOK(w, map[string]any{
			"ok":            true,
			"auth_disabled": true,
			"message":       "未设置 ADMIN_PASSWORD，管理接口免密（仅建议本机使用）",
		})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体必须是 JSON")
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(h.password())) != 1 {
		writeErr(w, http.StatusUnauthorized, "密码错误")
		return
	}
	h.setSessionCookie(w)
	writeOK(w, map[string]any{
		"ok":            true,
		"auth_disabled": false,
		"expires_at":    time.Now().Add(sessionTTL).Unix(),
	})
}

// handleSessionInfo 返回当前登录态（UI 据此决定展示登录页还是主界面）。
func (h *Handler) handleSessionInfo(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{
		"auth_disabled": h.password() == "",
		"authenticated": h.password() == "" || h.validSession(r),
	})
}

// handleLogout 清除会话 Cookie（额外提供，配合 UI「退出登录」）。
func (h *Handler) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
	writeOK(w, map[string]any{"ok": true})
}
