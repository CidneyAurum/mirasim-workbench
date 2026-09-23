package mirasim

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestMRS1RoundTrip(t *testing.T) {
	key := testKey(t)
	blob := EncryptMRS1("refresh-token-明文", key)
	if !strings.HasPrefix(blob, "mrs1:") {
		t.Fatalf("缺少 mrs1: 前缀: %q", blob)
	}
	got, err := DecryptMRS1(blob, key)
	if err != nil {
		t.Fatal(err)
	}
	if got != "refresh-token-明文" {
		t.Fatalf("往返不一致: %q", got)
	}
	// 空串往返
	if got, _ := DecryptMRS1(EncryptMRS1("", key), key); got != "" {
		t.Fatalf("空串往返失败: %q", got)
	}
}

func TestMRS1WrongKey(t *testing.T) {
	blob := EncryptMRS1("secret", testKey(t))
	if _, err := DecryptMRS1(blob, testKey(t)); err == nil {
		t.Fatal("错误 key 应解密失败")
	}
}

func TestMRS1PlaintextPassthrough(t *testing.T) {
	got, err := DecryptMRS1("not-encrypted", testKey(t))
	if err != nil || got != "not-encrypted" {
		t.Fatalf("非 mrs1: 输入应原样返回, got %q err %v", got, err)
	}
}

// 固定私钥（seed = 0x01..0x20）派生设备身份。
// 期望值由本实现首次运行产出后钉入（2026-09，手工核对过派生步骤：
// pub = base64(SPKI-DER)，id = base64url(sha256(pub 的 ASCII))[0:22]）。
func TestDeviceIdentityPinned(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	id, pub := DeviceIdentity(priv)
	if pub != "MCowBQYDK2VwAyEAebVWLo/mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ=" {
		t.Fatalf("publicKeyB64 不符: %q", pub)
	}
	if id != "JUdfAMozPES5oPh9G8P13N" {
		t.Fatalf("deviceID 不符: %q", id)
	}
	if len(id) != 22 {
		t.Fatalf("deviceID 长度应为 22: %d", len(id))
	}
}

func TestPrivateKeyPEMRoundTrip(t *testing.T) {
	priv, err := GenerateDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	pemStr, err := MarshalPrivateKeyPEM(priv)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePrivateKeyPEM(pemStr)
	if err != nil {
		t.Fatal(err)
	}
	if string(parsed) != string(priv) {
		t.Fatal("PEM 往返后私钥不一致")
	}
	if _, err := ParsePrivateKeyPEM("not a pem"); err == nil {
		t.Fatal("非法 PEM 应报错")
	}
}

// CanonicalString 精确串断言。sha256 期望值用系统 shasum 独立计算：
// sha256hex("")、sha256hex("test-credential")、sha256hex("a\x001\x00b\x002\x00")、sha256hex("hello body")。
func TestCanonicalStringExact(t *testing.T) {
	got := CanonicalString("post", "/v1/messages", 1758000000123, "nonce-abc", "dev-1", "0.0.322",
		"test-credential", nil, []byte("hello body"))
	want := strings.Join([]string{
		"mrs-sig-v2",
		"POST",
		"/v1/messages",
		"1758000000123",
		"nonce-abc",
		"dev-1",
		"0.0.322",
		"a6fbaf094063b9172c9019fa2d20143f8396b431ebe6264ede59bf7c18145a9a",
		"", // 无 meta：第 9 行为空行
		"6d9876f6d571676eb86f735ba9476da91ec5d0c52a69f6434c93f5c9e680210e",
	}, "\n")
	if got != want {
		t.Fatalf("canonical string 不符:\n%q\nwant:\n%q", got, want)
	}
	if len(strings.Split(got, "\n")) != 10 {
		t.Fatal("应为 10 行")
	}
}

func TestCanonicalStringMetaAndEmptyBody(t *testing.T) {
	// meta map 乱序传入，结果须按键排序确定
	got := CanonicalString("GET", "/v1/limits", 1, "n", "d", "0.0.322",
		"cred", map[string]string{"b": "2", "a": "1"}, nil)
	want := strings.Join([]string{
		"mrs-sig-v2", "GET", "/v1/limits", "1", "n", "d", "0.0.322",
		"55d91a3561684b32df5e58a0d91968b93798af4f924bba383e1c98625ec0c834", // sha256hex("cred")
		"37664b19301f46515688d5a22cb9ee1852e0b6443e28c7f36340a13962f0c4f7", // sha256hex("a\01\0b\02\0")
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", // sha256hex("")
	}, "\n")
	if got != want {
		t.Fatalf("canonical string 不符:\n%q\nwant:\n%q", got, want)
	}
}

func TestSignHeadersVerify(t *testing.T) {
	priv, err := GenerateDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	deviceID, _ := DeviceIdentity(priv)
	body := []byte(`{"model":"claude-opus-4-8"}`)
	hdrs := SignHeaders(priv, deviceID, SignParams{
		Method:        "POST",
		Path:          "/v1/messages",
		Credential:    "ticket-xyz",
		ClientVersion: "0.0.322",
		Body:          body,
	})
	for _, k := range []string{"x-mirasim-device", "x-mirasim-ts", "x-mirasim-nonce", "x-mirasim-sig", "x-mirasim-client"} {
		if hdrs[k] == "" {
			t.Fatalf("缺少头 %s", k)
		}
	}
	// 用返回的头重建 canonical string，公钥验签
	var ts int64
	if _, err := fmt.Sscanf(hdrs["x-mirasim-ts"], "%d", &ts); err != nil {
		t.Fatal(err)
	}
	canonical := CanonicalString("POST", "/v1/messages", ts, hdrs["x-mirasim-nonce"], deviceID,
		"0.0.322", "ticket-xyz", nil, body)
	sig, err := base64.RawURLEncoding.DecodeString(hdrs["x-mirasim-sig"])
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, []byte(canonical), sig) {
		t.Fatal("ed25519 验签失败")
	}
	// nonce 应为 12 字节 base64url
	if n, err := base64.RawURLEncoding.DecodeString(hdrs["x-mirasim-nonce"]); err != nil || len(n) != 12 {
		t.Fatalf("nonce 非法: %v len=%d", err, len(n))
	}
}

// TestSealHeadersRoundTrip 模拟 relay：生成 X25519 密钥对，Seal 后按
// PROTOCOL.md 反向解（临时公钥32 || nonce12 || 密文 || tag16），断言字段与布局。
func TestSealHeadersRoundTrip(t *testing.T) {
	relayPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	relayPubB64 := base64.StdEncoding.EncodeToString(relayPriv.PublicKey().Bytes())

	hdrs := map[string]string{
		"x-mirasim-device": "dev-123",
		"x-mirasim-ts":     "1758000000456",
		"x-mirasim-nonce":  "nonce-xyz",
		"x-mirasim-sig":    "sig-abc",
	}
	enc, err := SealHeaders(relayPubB64, "POST", "/v1/messages", hdrs)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	// 布局：临时公钥(32) || nonce(12) || 密文 || tag(16)
	if len(raw) < 32+12+16 {
		t.Fatalf("seal 输出过短: %d", len(raw))
	}
	ephPubRaw, nonce, ctWithTag := raw[:32], raw[32:44], raw[44:]
	ct := ctWithTag[:len(ctWithTag)-16]
	if len(raw) != 32+12+len(ct)+16 {
		t.Fatalf("布局总长不符: %d != %d", len(raw), 32+12+len(ct)+16)
	}
	ephPub, err := ecdh.X25519().NewPublicKey(ephPubRaw)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := relayPriv.ECDH(ephPub)
	if err != nil {
		t.Fatal(err)
	}
	hk := hkdf.New(sha256.New, shared, ephPubRaw, []byte("mrs-seal-v1"))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(hk, key); err != nil {
		t.Fatal(err)
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := aead.Open(nil, nonce, ctWithTag, []byte("mrs-seal-v1\nPOST\n/v1/messages"))
	if err != nil {
		t.Fatalf("relay 侧解密失败: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(pt, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"x-mirasim-device", "x-mirasim-ts", "x-mirasim-nonce", "x-mirasim-sig"} {
		if got[k] != hdrs[k] {
			t.Fatalf("字段 %s 不符: %q != %q", k, got[k], hdrs[k])
		}
	}
	if len(got) != 4 {
		t.Fatalf("应只有 4 个字段: %v", got)
	}
	// AAD 不匹配应解密失败
	if _, err := aead.Open(nil, nonce, ctWithTag, []byte("mrs-seal-v1\nGET\n/v1/messages")); err == nil {
		t.Fatal("AAD 不匹配应解密失败")
	}
}

func TestSealHeadersFieldOrder(t *testing.T) {
	relayPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	relayPubB64 := base64.StdEncoding.EncodeToString(relayPriv.PublicKey().Bytes())
	enc, err := SealHeaders(relayPubB64, "GET", "/v1/models", map[string]string{
		"x-mirasim-device": "d", "x-mirasim-ts": "1", "x-mirasim-nonce": "n", "x-mirasim-sig": "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(enc)
	ephPubRaw, nonce, ctWithTag := raw[:32], raw[32:44], raw[44:]
	ephPub, _ := ecdh.X25519().NewPublicKey(ephPubRaw)
	shared, _ := relayPriv.ECDH(ephPub)
	hk := hkdf.New(sha256.New, shared, ephPubRaw, []byte("mrs-seal-v1"))
	key := make([]byte, 32)
	io.ReadFull(hk, key)
	aead, _ := chacha20poly1305.New(key)
	pt, err := aead.Open(nil, nonce, ctWithTag, []byte("mrs-seal-v1\nGET\n/v1/models"))
	if err != nil {
		t.Fatal(err)
	}
	// 字段顺序须与参考实现一致（struct 序列化，非 map 字典序）
	want := `{"x-mirasim-device":"d","x-mirasim-ts":"1","x-mirasim-nonce":"n","x-mirasim-sig":"s"}`
	if string(pt) != want {
		t.Fatalf("plaintext 字段顺序不符:\n%s\nwant:\n%s", pt, want)
	}
}

func TestJWTClaims(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"a@b.c","plan":"pro","plan_exp":1760000000,"token_type":"refresh"}`))
	token := "header." + payload + ".sig"
	claims := JWTClaims(token)
	if claims["email"] != "a@b.c" || claims["plan"] != "pro" || claims["token_type"] != "refresh" {
		t.Fatalf("claims 不符: %v", claims)
	}
	if v, ok := claims["plan_exp"].(float64); !ok || int64(v) != 1760000000 {
		t.Fatalf("plan_exp 不符: %v", claims["plan_exp"])
	}
	if JWTClaims("not-a-jwt") != nil {
		t.Fatal("非法 token 应返回 nil")
	}
	if JWTClaims("a.!!!.c") != nil {
		t.Fatal("非法 payload 应返回 nil")
	}
}
