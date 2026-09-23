# mirasim2api 设计文档

以 [sub2api](https://github.com/Wei-Shaw/sub2api) 的产品架构为蓝本（多账号池、API Key 分发、
精确计费、智能调度、并发控制、管理后台），上游适配 [Mirasim](https://mirasim.ai) 订阅额度
（协议细节见 `docs/PROTOCOL.md`）。

## 技术栈

- **Go 1.22+**（与 sub2api 后端同语言），单二进制交付，管理后台静态资源 `embed` 内嵌
- **SQLite**（`modernc.org/sqlite`，纯 Go 无 cgo）替代 sub2api 的 PostgreSQL+Redis，
  保留其「数据库为唯一事实源」的体验，免去两套中间件的运维负担
- 依赖仅：`modernc.org/sqlite`、`golang.org/x/crypto`（chacha20poly1305/hkdf）。
  HTTP 用标准库（Go 1.22 ServeMux 支持方法+路径模式）

## 目录结构

```
cmd/server/main.go        # 入口：配置加载、DB 初始化、HTTP 服务装配
internal/config/          # 环境变量 + 可选 YAML 配置
internal/mirasim/         # 协议层：设备身份、mrs-sig-v2 签名、mrs-seal-v1 密封、
                          #   token 刷新（轮换回写）、ticket 申领、签名上游请求
internal/store/           # SQLite：accounts / api_keys / usage_logs / settings / device
internal/pool/            # 账号池：额度加权选号、会话粘性、熔断冷却、配额轮询
internal/gateway/         # 模型网关：/v1/messages /v1/responses /v1/chat/completions
                          #   /v1/models /v1/limits；请求规范化；SSE 透传；用量解析
internal/billing/         # 用量记录与费用计算（模型价格表 + 倍率）
internal/admin/           # 管理 API（会话认证）+ 内嵌 WebUI
web/                      # 管理后台静态资源（单页，原生 JS，无前端工具链）
deploy/                   # Dockerfile、docker-compose.yml、.env.example
docs/                     # 本文档与 PROTOCOL.md
```

## 数据库 Schema（SQLite）

```sql
accounts(id TEXT PK, email, name, provider, refresh_token_enc TEXT,  -- AES-256-GCM 密文
         enabled INTEGER, created_at, updated_at)
api_keys(id TEXT PK, name, key_hash TEXT UNIQUE, key_display TEXT,   -- 只存 sha256，展示 sk-...abcd
         enabled INTEGER, concurrency INTEGER,        -- 每 key 并发上限，0=默认
         rate_limit_rpm INTEGER,                      -- 每分钟请求数，0=不限
         model_allowlist TEXT,                         -- JSON 数组，空=全部（简化版分组）
         total_input_tokens, total_output_tokens, total_cost REAL,  -- 冗余累计，查仪表盘快
         expires_at, created_at, last_used_at)
usage_logs(id INTEGER PK AUTOINCREMENT, api_key_id, account_id, model, endpoint,
           input_tokens, output_tokens, cached_tokens, cost REAL,
           status INTEGER, err TEXT, duration_ms, created_at)
settings(key TEXT PK, value TEXT)      -- 运行时设置（代理、伪装模式、价格表等）
device(id INTEGER PK CHECK(id=1), private_key_enc TEXT)  -- 本实例 Ed25519 设备私钥（加密落盘）
```

## 核心机制（sub2api 经验 → 本项目落地）

| sub2api 概念 | 本项目实现 |
|---|---|
| 多账号管理（OAuth） | 控制台加号：GitHub/Google OAuth（loopback 自动回调 或 粘贴回调链接）+ 邮箱验证码；refresh token 加密落盘，轮换即回写 |
| API Key 分发 | `sk-` 前缀密钥，sha256 存储；每 key 并发上限 / RPM 上限 / 模型白名单 / 过期时间 |
| 精确计费 | 从响应（含 SSE 末帧）解析 usage：Anthropic `usage.{input,output,cache_read}_tokens`、Responses `usage.{input,output,cached}_tokens`；模型价格表（settings 可配）× 倍率计费；写 usage_logs 并累加到 api_keys |
| 智能调度 | 额度加权随机选号 + 会话粘性（prompt cache 友好）+ 指数退避熔断；见 PROTOCOL.md「选号与配额经验」 |
| 并发控制 | 每账号 inflight 信号量（默认 5）+ 每 API key 并发槽 |
| 限流 | 每 API key 每分钟请求数（内存滑动窗口） |
| 管理后台 | 内嵌单页 WebUI：仪表盘（用量/账号状态）、账号池、API Keys、用量日志、设置 |
| 分组 | 简化为 key 级模型白名单（多提供商分组路由对单一上游无意义） |

## 网关行为

1. **认证**：`Authorization: Bearer sk-...` 或 `x-api-key`；校验启用状态、过期、模型白名单、并发与限流。
2. **规范化**（按 PROTOCOL.md）：system 提顶层、剔 `top_p`（仅 /v1/messages）、剥 `mirasim/` 前缀、
   Responses 字符串 input 包装、claude-* 注入身份指纹 + 200 字节截断（relaxed/strict 可配）。
3. **转发**：选号 → 签名+密封 → 透传（SSE 管道直通，边转发边从流里捞 usage）。
4. **重试**：503 `model_capacity_exhausted` 同模型退避重试（默认 2 次，1.2s/2.4s），不熔断账号；
   401 换账号重试一次；上游 5xx 不熔断。
5. **计费**：请求结束后异步落 usage_logs；失败请求（relay 明确不计费的 503 等）记 0 费用。

## 配置（环境变量）

| 变量 | 默认 | 说明 |
|---|---|---|
| `PORT` / `HOST` | `8787` / `0.0.0.0` | 监听地址 |
| `DATA_DIR` | `./data` | SQLite 与密钥文件目录 |
| `MASTER_KEY` | 自动生成存 `DATA_DIR/master.key`（0600） | 64 位 hex，加密 refresh token 与设备私钥；**丢了数据就解不开** |
| `ADMIN_PASSWORD` | 空 | 管理后台密码；空则仅监听 loopback 时免密 |
| `GATEWAY_KEY_REQUIRED` | `true` | 模型请求是否必须带 sk- key |
| `RELAY_URL` / `AUTH_URL` | 官方地址 | 上游覆盖 |
| `MIRASIM_CLIENT_VERSION` | `0.0.322` | 参与签名的客户端版本 |
| `MIRASIM_SEAL_PUBKEY` | 内置值 | relay 密封公钥 |
| `UPSTREAM_PROXY` | 空 | 出站 HTTP CONNECT 代理 |
| `CLAUDE_CLOAK_MODE` | `relaxed` | `relaxed` / `strict` |
| `CAPACITY_RETRIES` / `CAPACITY_BACKOFF_MS` | `2` / `1200` | 503 同模型重试 |

## 安全

- refresh token、设备私钥一律 AES-256-GCM 加密落盘（`mrs1:` 风格 blob：base64(iv12|tag16|ct)）
- API key 只存 sha256；管理会话用 HttpOnly Cookie（HMAC 签名 token）
- 管理后台与模型网关同端口；`ADMIN_PASSWORD` 未设且绑定非 loopback 地址时拒绝启动并提示

## 免责声明

本项目仅供技术学习与研究。使用可能违反 Mirasim 及上游（Anthropic/OpenAI 等）的服务条款，
风险由使用者自负；不得用于商业运营。
