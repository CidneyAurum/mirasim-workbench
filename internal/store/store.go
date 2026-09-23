// Package store 提供 SQLite 持久化（accounts / api_keys / usage_logs / settings / device）。
// 密文字段（refresh_token、设备私钥）经 mirasim.EncryptMRS1 加密落盘。
package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"mirasim2api/internal/mirasim"
)

// Store 持有数据库连接与 master key。
type Store struct {
	db        *sql.DB
	masterKey []byte
}

const schema = `
CREATE TABLE IF NOT EXISTS accounts(
	id TEXT PRIMARY KEY,
	email TEXT,
	name TEXT,
	provider TEXT,
	refresh_token_enc TEXT,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_accounts_email_provider ON accounts(email, provider);

CREATE TABLE IF NOT EXISTS api_keys(
	id TEXT PRIMARY KEY,
	name TEXT,
	key_hash TEXT UNIQUE,
	key_display TEXT,
	enabled INTEGER NOT NULL DEFAULT 1,
	concurrency INTEGER NOT NULL DEFAULT 0,
	rate_limit_rpm INTEGER NOT NULL DEFAULT 0,
	model_allowlist TEXT,
	total_input_tokens INTEGER NOT NULL DEFAULT 0,
	total_output_tokens INTEGER NOT NULL DEFAULT 0,
	total_cost REAL NOT NULL DEFAULT 0,
	expires_at INTEGER,
	created_at INTEGER NOT NULL,
	last_used_at INTEGER
);

CREATE TABLE IF NOT EXISTS usage_logs(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	api_key_id TEXT,
	account_id TEXT,
	model TEXT,
	endpoint TEXT,
	input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	cached_tokens INTEGER NOT NULL DEFAULT 0,
	cost REAL NOT NULL DEFAULT 0,
	status INTEGER NOT NULL DEFAULT 0,
	err TEXT,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_usage_logs_key ON usage_logs(api_key_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_logs_account ON usage_logs(account_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_logs_model ON usage_logs(model, created_at);

CREATE TABLE IF NOT EXISTS settings(
	key TEXT PRIMARY KEY,
	value TEXT
);

CREATE TABLE IF NOT EXISTS device(
	id INTEGER PRIMARY KEY CHECK(id=1),
	private_key_enc TEXT
);
`

// Open 打开（不存在则创建）数据库并自动建表。masterKey 须为 32 字节。
func Open(dataDir string, masterKey []byte) (*Store, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("store: master key 须为 32 字节，当前 %d", len(masterKey))
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
		filepath.Join(dataDir, "mirasim2api.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite 单连接避免 SQLITE_BUSY
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: 建表失败: %w", err)
	}
	return &Store{db: db, masterKey: masterKey}, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------- accounts ----------

// Account 是一个 Mirasim 账号。
type Account struct {
	ID           string
	Email        string
	Name         string
	Provider     string
	RefreshToken string // 解密后的明文，仅内存中存在
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AddAccount 加号；同 email+provider 已存在则更新 token（幂等）。
func (s *Store) AddAccount(email, name, provider, refreshToken string) (*Account, error) {
	enc := mirasim.EncryptMRS1(refreshToken, s.masterKey)
	now := time.Now()
	var existingID string
	err := s.db.QueryRow(
		`SELECT id FROM accounts WHERE email = ? AND provider = ?`, email, provider,
	).Scan(&existingID)
	if err == nil {
		if _, err := s.db.Exec(
			`UPDATE accounts SET refresh_token_enc = ?, name = ?, updated_at = ? WHERE id = ?`,
			enc, name, now.Unix(), existingID,
		); err != nil {
			return nil, err
		}
		return s.getAccount(existingID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	id := randomID()
	if _, err := s.db.Exec(
		`INSERT INTO accounts(id, email, name, provider, refresh_token_enc, enabled, created_at, updated_at)
		 VALUES(?,?,?,?,?,1,?,?)`,
		id, email, name, provider, enc, now.Unix(), now.Unix(),
	); err != nil {
		return nil, err
	}
	return s.getAccount(id)
}

func (s *Store) scanAccount(row *sql.Row) (*Account, error) {
	var a Account
	var enc string
	var enabled int
	var createdAt, updatedAt int64
	if err := row.Scan(&a.ID, &a.Email, &a.Name, &a.Provider, &enc, &enabled, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	tok, err := mirasim.DecryptMRS1(enc, s.masterKey)
	if err != nil {
		return nil, fmt.Errorf("store: 账号 %s token 解密失败: %w", a.ID, err)
	}
	a.RefreshToken = tok
	a.Enabled = enabled != 0
	a.CreatedAt = time.Unix(createdAt, 0)
	a.UpdatedAt = time.Unix(updatedAt, 0)
	return &a, nil
}

func (s *Store) getAccount(id string) (*Account, error) {
	return s.scanAccount(s.db.QueryRow(
		`SELECT id, email, name, provider, refresh_token_enc, enabled, created_at, updated_at
		 FROM accounts WHERE id = ?`, id))
}

// GetAccount 按 ID 取账号（含解密后的 refresh token）。
func (s *Store) GetAccount(id string) (*Account, error) { return s.getAccount(id) }

// UpdateAccountToken 轮换回写新 refresh token。
func (s *Store) UpdateAccountToken(id, newRefreshToken string) error {
	enc := mirasim.EncryptMRS1(newRefreshToken, s.masterKey)
	_, err := s.db.Exec(
		`UPDATE accounts SET refresh_token_enc = ?, updated_at = ? WHERE id = ?`,
		enc, time.Now().Unix(), id)
	return err
}

// SetAccountEnabled 启用/停用账号。
func (s *Store) SetAccountEnabled(id string, enabled bool) error {
	_, err := s.db.Exec(
		`UPDATE accounts SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(enabled), time.Now().Unix(), id)
	return err
}

// ListAccounts 列出全部账号（按创建时间升序）。
func (s *Store) ListAccounts() ([]*Account, error) {
	rows, err := s.db.Query(
		`SELECT id, email, name, provider, refresh_token_enc, enabled, created_at, updated_at
		 FROM accounts ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		var a Account
		var enc string
		var enabled int
		var createdAt, updatedAt int64
		if err := rows.Scan(&a.ID, &a.Email, &a.Name, &a.Provider, &enc, &enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		tok, err := mirasim.DecryptMRS1(enc, s.masterKey)
		if err != nil {
			return nil, fmt.Errorf("store: 账号 %s token 解密失败: %w", a.ID, err)
		}
		a.RefreshToken = tok
		a.Enabled = enabled != 0
		a.CreatedAt = time.Unix(createdAt, 0)
		a.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, &a)
	}
	return out, rows.Err()
}

// DeleteAccount 删除账号。
func (s *Store) DeleteAccount(id string) error {
	_, err := s.db.Exec(`DELETE FROM accounts WHERE id = ?`, id)
	return err
}

// ---------- device ----------

// LoadOrCreateDeviceKey 加载设备密钥；没有则生成并加密落库。
// 返回私钥与派生身份（deviceID、publicKeyB64）。
func (s *Store) LoadOrCreateDeviceKey() (ed25519.PrivateKey, string, string, error) {
	var enc string
	err := s.db.QueryRow(`SELECT private_key_enc FROM device WHERE id = 1`).Scan(&enc)
	if err == nil {
		pemStr, err := mirasim.DecryptMRS1(enc, s.masterKey)
		if err != nil {
			return nil, "", "", fmt.Errorf("store: 设备私钥解密失败: %w", err)
		}
		priv, err := mirasim.ParsePrivateKeyPEM(pemStr)
		if err != nil {
			return nil, "", "", err
		}
		deviceID, pubB64 := mirasim.DeviceIdentity(priv)
		return priv, deviceID, pubB64, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, "", "", err
	}
	priv, err := mirasim.GenerateDeviceKey()
	if err != nil {
		return nil, "", "", err
	}
	pemStr, err := mirasim.MarshalPrivateKeyPEM(priv)
	if err != nil {
		return nil, "", "", err
	}
	if _, err := s.db.Exec(
		`INSERT INTO device(id, private_key_enc) VALUES(1, ?)`,
		mirasim.EncryptMRS1(pemStr, s.masterKey),
	); err != nil {
		return nil, "", "", err
	}
	deviceID, pubB64 := mirasim.DeviceIdentity(priv)
	return priv, deviceID, pubB64, nil
}

// ---------- api_keys ----------

// APIKey 是一个分发的 sk- 密钥（只存 sha256）。
type APIKey struct {
	ID                string
	Name              string
	KeyDisplay        string
	Enabled           bool
	Concurrency       int
	RateLimitRPM      int
	ModelAllowlist    []string
	TotalInputTokens  int64
	TotalOutputTokens int64
	TotalCost         float64
	ExpiresAt         *time.Time
	CreatedAt         time.Time
	LastUsedAt        *time.Time
}

// APIKeyOpts 是 CreateAPIKey 的可选项。
type APIKeyOpts struct {
	Concurrency    int
	RateLimitRPM   int
	ModelAllowlist []string
	ExpiresAt      *time.Time
}

func hashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// CreateAPIKey 生成 "sk-" + 32 字节 hex 的明文 key（仅创建时返回），库存 sha256。
func (s *Store) CreateAPIKey(name string, opts APIKeyOpts) (string, *APIKey, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	plaintext := "sk-" + hex.EncodeToString(raw)
	allowlistJSON, err := json.Marshal(opts.ModelAllowlist)
	if err != nil {
		return "", nil, err
	}
	if opts.ModelAllowlist == nil {
		allowlistJSON = nil
	}
	id := randomID()
	now := time.Now()
	display := plaintext
	if len(plaintext) > 7 {
		display = plaintext[:7] + "..." + plaintext[len(plaintext)-4:]
	}
	var expiresAtUnix *int64
	if opts.ExpiresAt != nil {
		v := opts.ExpiresAt.Unix()
		expiresAtUnix = &v
	}
	if _, err := s.db.Exec(
		`INSERT INTO api_keys(id, name, key_hash, key_display, enabled, concurrency, rate_limit_rpm,
			model_allowlist, expires_at, created_at)
		 VALUES(?,?,?,?,1,?,?,?,?,?)`,
		id, name, hashAPIKey(plaintext), display, opts.Concurrency, opts.RateLimitRPM,
		string(allowlistJSON), expiresAtUnix, now.Unix(),
	); err != nil {
		return "", nil, err
	}
	rec, err := s.getAPIKey(id)
	if err != nil {
		return "", nil, err
	}
	return plaintext, rec, nil
}

func unixPtr(v *int64) *time.Time {
	if v == nil {
		return nil
	}
	t := time.Unix(*v, 0)
	return &t
}

func (s *Store) scanAPIKey(scanner interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	var enabled int
	var allowlist sql.NullString
	var expiresAt, lastUsedAt *int64
	var createdAt int64
	if err := scanner.Scan(&k.ID, &k.Name, &k.KeyDisplay, &enabled, &k.Concurrency, &k.RateLimitRPM,
		&allowlist, &k.TotalInputTokens, &k.TotalOutputTokens, &k.TotalCost,
		&expiresAt, &createdAt, &lastUsedAt); err != nil {
		return nil, err
	}
	k.Enabled = enabled != 0
	if allowlist.Valid && allowlist.String != "" {
		if err := json.Unmarshal([]byte(allowlist.String), &k.ModelAllowlist); err != nil {
			return nil, err
		}
	}
	k.ExpiresAt = unixPtr(expiresAt)
	k.CreatedAt = time.Unix(createdAt, 0)
	k.LastUsedAt = unixPtr(lastUsedAt)
	return &k, nil
}

const apiKeyCols = `id, name, key_display, enabled, concurrency, rate_limit_rpm,
	model_allowlist, total_input_tokens, total_output_tokens, total_cost, expires_at, created_at, last_used_at`

func (s *Store) getAPIKey(id string) (*APIKey, error) {
	return s.scanAPIKey(s.db.QueryRow(`SELECT `+apiKeyCols+` FROM api_keys WHERE id = ?`, id))
}

// GetAPIKeyByPlaintext 用明文 key 的 sha256 查找。
func (s *Store) GetAPIKeyByPlaintext(plaintext string) (*APIKey, error) {
	return s.scanAPIKey(s.db.QueryRow(`SELECT `+apiKeyCols+` FROM api_keys WHERE key_hash = ?`, hashAPIKey(plaintext)))
}

// ListAPIKeys 列出全部 key。
func (s *Store) ListAPIKeys() ([]*APIKey, error) {
	rows, err := s.db.Query(`SELECT ` + apiKeyCols + ` FROM api_keys ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		k, err := s.scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// APIKeyUpdate 是 UpdateAPIKey 的可变字段（nil 表示不改）。
type APIKeyUpdate struct {
	Enabled        *bool
	Concurrency    *int
	RateLimitRPM   *int
	ModelAllowlist *[]string
	ExpiresAt      *time.Time
}

// UpdateAPIKey 更新 key 的可变字段。
func (s *Store) UpdateAPIKey(id string, u APIKeyUpdate) error {
	if u.Enabled != nil {
		if _, err := s.db.Exec(`UPDATE api_keys SET enabled = ? WHERE id = ?`, boolToInt(*u.Enabled), id); err != nil {
			return err
		}
	}
	if u.Concurrency != nil {
		if _, err := s.db.Exec(`UPDATE api_keys SET concurrency = ? WHERE id = ?`, *u.Concurrency, id); err != nil {
			return err
		}
	}
	if u.RateLimitRPM != nil {
		if _, err := s.db.Exec(`UPDATE api_keys SET rate_limit_rpm = ? WHERE id = ?`, *u.RateLimitRPM, id); err != nil {
			return err
		}
	}
	if u.ModelAllowlist != nil {
		b, err := json.Marshal(*u.ModelAllowlist)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE api_keys SET model_allowlist = ? WHERE id = ?`, string(b), id); err != nil {
			return err
		}
	}
	if u.ExpiresAt != nil {
		v := u.ExpiresAt.Unix()
		if _, err := s.db.Exec(`UPDATE api_keys SET expires_at = ? WHERE id = ?`, v, id); err != nil {
			return err
		}
	}
	return nil
}

// DeleteAPIKey 删除 key。
func (s *Store) DeleteAPIKey(id string) error {
	_, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

// AddUsage 累加 key 的冗余统计字段。
func (s *Store) AddUsage(keyID string, input, output int64, cost float64) error {
	_, err := s.db.Exec(
		`UPDATE api_keys SET total_input_tokens = total_input_tokens + ?,
			total_output_tokens = total_output_tokens + ?, total_cost = total_cost + ?
		 WHERE id = ?`, input, output, cost, keyID)
	return err
}

// TouchAPIKeyUsed 更新 last_used_at。
func (s *Store) TouchAPIKeyUsed(keyID string) error {
	_, err := s.db.Exec(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), keyID)
	return err
}

// ---------- usage_logs ----------

// UsageLog 是一条用量记录。
type UsageLog struct {
	ID           int64
	APIKeyID     string
	AccountID    string
	Model        string
	Endpoint     string
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	Cost         float64
	Status       int
	Err          string
	DurationMs   int64
	CreatedAt    time.Time
}

// AddUsageLog 写入一条用量日志。
func (s *Store) AddUsageLog(l *UsageLog) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO usage_logs(api_key_id, account_id, model, endpoint, input_tokens, output_tokens,
			cached_tokens, cost, status, err, duration_ms, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.APIKeyID, l.AccountID, l.Model, l.Endpoint, l.InputTokens, l.OutputTokens,
		l.CachedTokens, l.Cost, l.Status, l.Err, l.DurationMs, time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UsageLogFilter 是 QueryUsageLogs 的过滤条件。
type UsageLogFilter struct {
	KeyID     string
	AccountID string
	Model     string
	Since     time.Time
	Limit     int
	Offset    int
}

// usageLogWhere 拼过滤条件（QueryUsageLogs 与 CountUsageLogs 共用）。
func usageLogWhere(f UsageLogFilter) (string, []any) {
	q := ` FROM usage_logs WHERE 1=1`
	var args []any
	if f.KeyID != "" {
		q += ` AND api_key_id = ?`
		args = append(args, f.KeyID)
	}
	if f.AccountID != "" {
		q += ` AND account_id = ?`
		args = append(args, f.AccountID)
	}
	if f.Model != "" {
		q += ` AND model = ?`
		args = append(args, f.Model)
	}
	if !f.Since.IsZero() {
		q += ` AND created_at >= ?`
		args = append(args, f.Since.Unix())
	}
	return q, args
}

// QueryUsageLogs 按过滤条件查询（按创建时间倒序）。
func (s *Store) QueryUsageLogs(f UsageLogFilter) ([]*UsageLog, error) {
	where, args := usageLogWhere(f)
	q := `SELECT id, api_key_id, account_id, model, endpoint, input_tokens, output_tokens,
		cached_tokens, cost, status, err, duration_ms, created_at` + where
	q += ` ORDER BY created_at DESC, id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	if f.Offset > 0 {
		if f.Limit <= 0 {
			q += ` LIMIT -1`
		}
		q += ` OFFSET ?`
		args = append(args, f.Offset)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UsageLog
	for rows.Next() {
		var l UsageLog
		var createdAt int64
		if err := rows.Scan(&l.ID, &l.APIKeyID, &l.AccountID, &l.Model, &l.Endpoint,
			&l.InputTokens, &l.OutputTokens, &l.CachedTokens, &l.Cost, &l.Status,
			&l.Err, &l.DurationMs, &createdAt); err != nil {
			return nil, err
		}
		l.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, &l)
	}
	return out, rows.Err()
}

// CountUsageLogs 统计过滤条件下的日志总数（分页用）。
func (s *Store) CountUsageLogs(f UsageLogFilter) (int64, error) {
	where, args := usageLogWhere(f)
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*)`+where, args...).Scan(&n)
	return n, err
}

// UsageSummaryRow 是按 model+key 聚合的一行汇总。
type UsageSummaryRow struct {
	Model        string
	APIKeyID     string
	Requests     int64
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	Cost         float64
}

// UsageSummary 聚合 since 以来的用量（按 model + key 分组）。
func (s *Store) UsageSummary(since time.Time) ([]*UsageSummaryRow, error) {
	q := `SELECT model, api_key_id, COUNT(*), SUM(input_tokens), SUM(output_tokens),
		SUM(cached_tokens), SUM(cost) FROM usage_logs`
	var args []any
	if !since.IsZero() {
		q += ` WHERE created_at >= ?`
		args = append(args, since.Unix())
	}
	q += ` GROUP BY model, api_key_id ORDER BY SUM(cost) DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UsageSummaryRow
	for rows.Next() {
		var r UsageSummaryRow
		if err := rows.Scan(&r.Model, &r.APIKeyID, &r.Requests, &r.InputTokens,
			&r.OutputTokens, &r.CachedTokens, &r.Cost); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ---------- settings ----------

// GetSetting 读设置；不存在返回空串与 nil。
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting 写设置（upsert）。
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings(key, value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ListSettings 列出全部设置。
func (s *Store) ListSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
