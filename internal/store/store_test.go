package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	s, err := Open(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func TestAccountsCRUD(t *testing.T) {
	s, _ := openTemp(t)
	a, err := s.AddAccount("u@example.com", "主号", "github", "rt-plain-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || !a.Enabled || a.RefreshToken != "rt-plain-1" {
		t.Fatalf("账号字段不符: %+v", a)
	}

	got, err := s.GetAccount(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "u@example.com" || got.RefreshToken != "rt-plain-1" {
		t.Fatalf("读回不符: %+v", got)
	}

	// refresh token 落库须为密文
	var enc string
	if err := s.db.QueryRow(`SELECT refresh_token_enc FROM accounts WHERE id = ?`, a.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "mrs1:") || strings.Contains(enc, "rt-plain-1") {
		t.Fatalf("落库的不是密文: %q", enc)
	}

	// 幂等加号：同 email+provider 更新 token
	a2, err := s.AddAccount("u@example.com", "改名", "github", "rt-plain-2")
	if err != nil {
		t.Fatal(err)
	}
	if a2.ID != a.ID {
		t.Fatalf("幂等加号应复用 ID: %q != %q", a2.ID, a.ID)
	}
	if a2.RefreshToken != "rt-plain-2" || a2.Name != "改名" {
		t.Fatalf("幂等更新未生效: %+v", a2)
	}
	list, _ := s.ListAccounts()
	if len(list) != 1 {
		t.Fatalf("应只有 1 个账号: %d", len(list))
	}

	if err := s.UpdateAccountToken(a.ID, "rt-plain-3"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAccount(a.ID)
	if got.RefreshToken != "rt-plain-3" {
		t.Fatalf("UpdateAccountToken 未生效: %q", got.RefreshToken)
	}

	if err := s.SetAccountEnabled(a.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAccount(a.ID)
	if got.Enabled {
		t.Fatal("SetAccountEnabled(false) 未生效")
	}

	if err := s.DeleteAccount(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAccount(a.ID); err != sql.ErrNoRows {
		t.Fatalf("删除后应为 ErrNoRows: %v", err)
	}
}

func TestDeviceKeyPersistence(t *testing.T) {
	s, dir := openTemp(t)
	priv1, id1, pub1, err := s.LoadOrCreateDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(priv1) == 0 || id1 == "" || pub1 == "" {
		t.Fatal("设备身份为空")
	}
	// 私钥落库须为密文
	var enc string
	if err := s.db.QueryRow(`SELECT private_key_enc FROM device WHERE id = 1`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "mrs1:") || strings.Contains(enc, "PRIVATE KEY") {
		t.Fatalf("设备私钥落库的不是密文: %q", enc[:30])
	}

	// 重开库（同 master key）应加载同一把钥匙
	s.Close()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	s2, err := Open(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	priv2, id2, pub2, err := s2.LoadOrCreateDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 || pub1 != pub2 || string(priv1) != string(priv2) {
		t.Fatal("重开后设备身份不一致")
	}
	if _, err := os.Stat(filepath.Join(dir, "mirasim2api.db")); err != nil {
		t.Fatal("数据库文件未创建")
	}
}

func TestAPIKeysCRUD(t *testing.T) {
	s, _ := openTemp(t)
	exp := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	plain, rec, err := s.CreateAPIKey("测试 key", APIKeyOpts{
		Concurrency:    3,
		RateLimitRPM:   60,
		ModelAllowlist: []string{"claude-*"},
		ExpiresAt:      &exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "sk-") || len(plain) != 3+64 {
		t.Fatalf("明文 key 格式不符: %q", plain)
	}
	if !rec.Enabled || rec.Concurrency != 3 || rec.RateLimitRPM != 60 {
		t.Fatalf("key 字段不符: %+v", rec)
	}
	if len(rec.ModelAllowlist) != 1 || rec.ModelAllowlist[0] != "claude-*" {
		t.Fatalf("白名单不符: %v", rec.ModelAllowlist)
	}
	if rec.ExpiresAt == nil || !rec.ExpiresAt.Equal(exp) {
		t.Fatalf("过期时间不符: %v", rec.ExpiresAt)
	}

	// 库存 sha256，不含明文
	var hash string
	if err := s.db.QueryRow(`SELECT key_hash FROM api_keys WHERE id = ?`, rec.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, plain) || len(hash) != 64 {
		t.Fatalf("key_hash 异常: %q", hash)
	}

	// 明文查找往返
	got, err := s.GetAPIKeyByPlaintext(plain)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != rec.ID {
		t.Fatalf("明文查找命中错误的 key: %q", got.ID)
	}
	if _, err := s.GetAPIKeyByPlaintext("sk-nonexistent"); err != sql.ErrNoRows {
		t.Fatalf("不存在应返回 ErrNoRows: %v", err)
	}

	// 更新
	off := false
	newLimit := 120
	if err := s.UpdateAPIKey(rec.ID, APIKeyUpdate{Enabled: &off, RateLimitRPM: &newLimit}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAPIKeyByPlaintext(plain)
	if got.Enabled || got.RateLimitRPM != 120 || got.Concurrency != 3 {
		t.Fatalf("更新不符: %+v", got)
	}

	// 用量累计与 last_used
	if err := s.AddUsage(rec.ID, 10, 20, 0.5); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(rec.ID, 5, 5, 0.25); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchAPIKeyUsed(rec.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAPIKeyByPlaintext(plain)
	if got.TotalInputTokens != 15 || got.TotalOutputTokens != 25 || got.TotalCost != 0.75 {
		t.Fatalf("累计不符: %+v", got)
	}
	if got.LastUsedAt == nil {
		t.Fatal("last_used_at 未更新")
	}

	list, _ := s.ListAPIKeys()
	if len(list) != 1 {
		t.Fatalf("应只有 1 个 key: %d", len(list))
	}
	if err := s.DeleteAPIKey(rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAPIKeyByPlaintext(plain); err != sql.ErrNoRows {
		t.Fatalf("删除后应为 ErrNoRows: %v", err)
	}
}

func TestUsageLogs(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Now()
	for i := range 5 {
		model := "claude-a"
		if i%2 == 0 {
			model = "gpt-b"
		}
		if _, err := s.AddUsageLog(&UsageLog{
			APIKeyID:     fmt.Sprintf("key-%d", i%2),
			AccountID:    "acc-1",
			Model:        model,
			Endpoint:     "/v1/messages",
			InputTokens:  int64(10 * (i + 1)),
			OutputTokens: int64(i + 1),
			CachedTokens: 2,
			Cost:         0.1 * float64(i+1),
			Status:       200,
		}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.QueryUsageLogs(UsageLogFilter{Limit: 100})
	if err != nil || len(all) != 5 {
		t.Fatalf("查询失败: %v n=%d", err, len(all))
	}
	// 倒序
	for i := 1; i < len(all); i++ {
		if all[i-1].CreatedAt.Before(all[i].CreatedAt) {
			t.Fatal("应按创建时间倒序")
		}
	}
	byKey, _ := s.QueryUsageLogs(UsageLogFilter{KeyID: "key-0"})
	if len(byKey) != 3 {
		t.Fatalf("按 key 过滤: %d", len(byKey))
	}
	byModel, _ := s.QueryUsageLogs(UsageLogFilter{Model: "gpt-b"})
	if len(byModel) != 3 {
		t.Fatalf("按 model 过滤: %d", len(byModel))
	}
	paged, _ := s.QueryUsageLogs(UsageLogFilter{Limit: 2, Offset: 4})
	if len(paged) != 1 {
		t.Fatalf("分页: %d", len(paged))
	}
	future, _ := s.QueryUsageLogs(UsageLogFilter{Since: now.Add(time.Hour)})
	if len(future) != 0 {
		t.Fatalf("since 过滤: %d", len(future))
	}

	sum, err := s.UsageSummary(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum) != 2 { // 按 model+key 分组：(gpt-b,key-0)×3、(claude-a,key-1)×2
		t.Fatalf("聚合行数: %d (%+v)", len(sum), sum)
	}
	var totalIn, totalOut, totalCached, totalReq int64
	var totalCost float64
	for _, r := range sum {
		totalIn += r.InputTokens
		totalOut += r.OutputTokens
		totalCached += r.CachedTokens
		totalCost += r.Cost
		totalReq += r.Requests
	}
	if totalReq != 5 || totalIn != 150 || totalOut != 15 || totalCached != 10 {
		t.Fatalf("聚合值不符: %+v", sum)
	}
	if totalCost < 1.49 || totalCost > 1.51 {
		t.Fatalf("聚合费用不符: %f", totalCost)
	}
}

func TestSettings(t *testing.T) {
	s, _ := openTemp(t)
	if v, err := s.GetSetting("missing"); err != nil || v != "" {
		t.Fatalf("缺失设置应返回空串: %q %v", v, err)
	}
	if err := s.SetSetting("proxy", "http://127.0.0.1:7890"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("cloak", "strict"); err != nil {
		t.Fatal(err)
	}
	// upsert
	if err := s.SetSetting("cloak", "relaxed"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting("cloak"); v != "relaxed" {
		t.Fatalf("upsert 未生效: %q", v)
	}
	all, err := s.ListSettings()
	if err != nil || len(all) != 2 || all["proxy"] != "http://127.0.0.1:7890" {
		t.Fatalf("ListSettings: %v %v", all, err)
	}
}

func TestOpenBadMasterKey(t *testing.T) {
	if _, err := Open(t.TempDir(), []byte("short")); err == nil {
		t.Fatal("短 master key 应报错")
	}
}
