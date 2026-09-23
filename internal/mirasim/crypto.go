// Package mirasim 实现 Mirasim relay 协议（mrs-sig-v2 签名 + mrs-seal-v1 密封）。
// 协议细节见 docs/PROTOCOL.md，本包实现须与其逐字节等价。
package mirasim

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// mrs1 密文布局：base64(iv(12) || tag(16) || ciphertext)
const mrs1Prefix = "mrs1:"

// EncryptMRS1 用 AES-256-GCM 加密，输出 "mrs1:" + base64(iv||tag||ct)。
func EncryptMRS1(plaintext string, key []byte) string {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(fmt.Sprintf("mirasim: 非法 master key 长度 %d", len(key)))
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	iv := make([]byte, gcm.NonceSize()) // 12
	if _, err := rand.Read(iv); err != nil {
		panic(err)
	}
	// Seal 输出 ct||tag，需要调整为 iv||tag||ct
	sealed := gcm.Seal(nil, iv, []byte(plaintext), nil)
	ct, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	out := make([]byte, 0, len(iv)+len(tag)+len(ct))
	out = append(out, iv...)
	out = append(out, tag...)
	out = append(out, ct...)
	return mrs1Prefix + base64.StdEncoding.EncodeToString(out)
}

// DecryptMRS1 解密 mrs1 blob；非 "mrs1:" 前缀的输入原样返回（兼容明文历史数据）。
func DecryptMRS1(blob string, key []byte) (string, error) {
	if !strings.HasPrefix(blob, mrs1Prefix) {
		return blob, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(blob, mrs1Prefix))
	if err != nil {
		return "", fmt.Errorf("mirasim: mrs1 base64 解码失败: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("mirasim: 非法 master key 长度 %d", len(key))
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns, ov := gcm.NonceSize(), gcm.Overhead()
	if len(raw) < ns+ov {
		return "", errors.New("mirasim: mrs1 blob 过短")
	}
	iv, tag, ct := raw[:ns], raw[ns:ns+ov], raw[ns+ov:]
	sealed := append(append([]byte{}, ct...), tag...)
	pt, err := gcm.Open(nil, iv, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("mirasim: mrs1 解密失败: %w", err)
	}
	return string(pt), nil
}

// GenerateDeviceKey 生成全新的 Ed25519 设备密钥。
func GenerateDeviceKey() (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	return priv, err
}

// DeviceIdentity 派生设备身份：
// publicKeyB64 = base64(SPKI/PKIX DER)；
// deviceID = base64url(sha256(publicKeyB64 的 ASCII 字节))[0:22]。
func DeviceIdentity(priv ed25519.PrivateKey) (deviceID string, publicKeyB64 string) {
	pub := priv.Public().(ed25519.PublicKey)
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		panic(err)
	}
	publicKeyB64 = base64.StdEncoding.EncodeToString(der)
	sum := sha256.Sum256([]byte(publicKeyB64))
	deviceID = base64.RawURLEncoding.EncodeToString(sum[:])[:22]
	return deviceID, publicKeyB64
}

// MarshalPrivateKeyPEM 序列化 Ed25519 私钥为 PKCS8 PEM。
func MarshalPrivateKeyPEM(priv ed25519.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

// ParsePrivateKeyPEM 解析 PKCS8 PEM 的 Ed25519 私钥。
func ParsePrivateKeyPEM(pemStr string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("mirasim: PEM 解码失败")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("mirasim: 非 Ed25519 私钥: %T", key)
	}
	return priv, nil
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CanonicalString 构造 mrs-sig-v2 签名原文（10 行，\n 连接）。
// meta 为空时第 9 行留空（空行，不是空串哈希）；非空时按键排序后
// sha256hex("k1\0v1\0k2\0v2")。body 为空时是空字节的哈希。
func CanonicalString(method, path string, ts int64, nonce, deviceID, clientVersion, credential string, meta map[string]string, body []byte) string {
	metaLine := ""
	if len(meta) > 0 {
		keys := make([]string, 0, len(meta))
		for k := range meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for _, k := range keys {
			sb.WriteString(k)
			sb.WriteByte(0)
			sb.WriteString(meta[k])
			sb.WriteByte(0)
		}
		metaLine = sha256hex([]byte(sb.String()))
	}
	lines := []string{
		"mrs-sig-v2",
		strings.ToUpper(method),
		path,
		strconv.FormatInt(ts, 10),
		nonce,
		deviceID,
		clientVersion,
		sha256hex([]byte(credential)),
		metaLine,
		sha256hex(body),
	}
	return strings.Join(lines, "\n")
}

// SignParams 是 SignHeaders 的入参。
type SignParams struct {
	Method        string
	Path          string
	Credential    string // /v1/device/session 用 access token；模型路由用 ticket
	ClientVersion string
	Meta          map[string]string
	Body          []byte
}

// SignHeaders 生成 5 个明文签名头（仅 /v1/device/session 这样发）。
func SignHeaders(priv ed25519.PrivateKey, deviceID string, p SignParams) map[string]string {
	ts := nowMillis()
	nonceRaw := make([]byte, 12)
	if _, err := rand.Read(nonceRaw); err != nil {
		panic(err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	canonical := CanonicalString(p.Method, p.Path, ts, nonce, deviceID, p.ClientVersion, p.Credential, p.Meta, p.Body)
	sig := ed25519.Sign(priv, []byte(canonical))
	return map[string]string{
		"x-mirasim-device": deviceID,
		"x-mirasim-ts":     strconv.FormatInt(ts, 10),
		"x-mirasim-nonce":  nonce,
		"x-mirasim-sig":    base64.RawURLEncoding.EncodeToString(sig),
		"x-mirasim-client": p.ClientVersion,
	}
}

// sealPlaintext 按参考实现的字段顺序序列化（不用 map，保证字节一致）。
type sealPlaintext struct {
	Device string `json:"x-mirasim-device"`
	TS     string `json:"x-mirasim-ts"`
	Nonce  string `json:"x-mirasim-nonce"`
	Sig    string `json:"x-mirasim-sig"`
}

// SealHeaders 实现 mrs-seal-v1：把 4 个签名头密封进 x-mirasim-enc 的值。
// 布局：临时公钥(32) || nonce(12) || 密文 || tag(16)，整体 base64url。
func SealHeaders(relayPubkeyB64 string, method, path string, headers map[string]string) (string, error) {
	relayPubRaw, err := base64.StdEncoding.DecodeString(relayPubkeyB64)
	if err != nil {
		return "", fmt.Errorf("mirasim: seal 公钥 base64 解码失败: %w", err)
	}
	relayPub, err := ecdh.X25519().NewPublicKey(relayPubRaw)
	if err != nil {
		return "", fmt.Errorf("mirasim: seal 公钥非法: %w", err)
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := eph.ECDH(relayPub)
	if err != nil {
		return "", err
	}
	ephPub := eph.PublicKey().Bytes()
	// key = HKDF-SHA256(ikm=shared, salt=临时公钥, info="mrs-seal-v1", L=32)
	hk := hkdf.New(sha256.New, shared, ephPub, []byte("mrs-seal-v1"))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(hk, key); err != nil {
		return "", err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize()) // 12
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	pt, err := json.Marshal(sealPlaintext{
		Device: headers["x-mirasim-device"],
		TS:     headers["x-mirasim-ts"],
		Nonce:  headers["x-mirasim-nonce"],
		Sig:    headers["x-mirasim-sig"],
	})
	if err != nil {
		return "", err
	}
	aad := []byte("mrs-seal-v1\n" + strings.ToUpper(method) + "\n" + path)
	sealed := aead.Seal(nil, nonce, pt, aad) // ct||tag
	out := make([]byte, 0, len(ephPub)+len(nonce)+len(sealed))
	out = append(out, ephPub...)
	out = append(out, nonce...)
	out = append(out, sealed...)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// JWTClaims 解 JWT payload（不验签），用于本地读取 plan/plan_exp/email/token_type。
func JWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}
