# Mirasim 协议规格（逆向所得，经真实抓包验证）

> 来源：CharTyr/mira2api 项目（适配 Mirasim v0.0.322，2026-09 协议）的公开逆向成果。
> 本项目（mirasim2api）的 Go 实现必须与本文档逐字节等价。

## 端点

- relay（模型中转）：`https://relay.mirasim.ai`（可用 `RELAY_URL` 覆盖）
- auth（认证后端）：`https://auth.mirasim.ai`（可用 `AUTH_URL` 覆盖）
- 已废弃：`mirasim-relay.mirofish.ai`、`admin.test.mirofish.ai`

## 凭据链

```
refresh token (JWT, 30天, 每次刷新都轮换, 含 plan/plan_exp 声明)
    │  POST {auth}/auth/refresh  {"refresh_token": <jwt>}
    ▼
access token (JWT, 1小时)        ← 过期前 120s 主动刷新；轮换后的新 refresh token 必须立即持久化
    │  POST {relay}/v1/device/session  {"publicKey": <spki-der-b64>, "deviceId": <id>}
    │  头: Bearer access token + 明文 mrs-sig-v2 签名头（credential = access token）
    ▼
device ticket (900秒 TTL)        ← 提前 60s 重领
    │  模型请求: Bearer ticket + x-mirasim-client + x-mirasim-enc（密封的签名头）
    ▼
relay 模型路由
```

## 设备身份

- 每个实例自持一对 **Ed25519** 设备密钥（全新生成即可，无需借用 Mirasim 客户端的）。
- `publicKeyB64 = base64(SPKI-DER)`（标准 PKIX 编码）
- `deviceId = base64url(sha256(publicKeyB64 的 ASCII 字节))[0:22]`
  （注意：sha256 的输入是 base64 字符串本身，不是 DER 字节）

## mrs-sig-v2 签名

签名原文（`\n` 连接，共 10 行）：

```
mrs-sig-v2
{METHOD}                  ← 大写
{PATH}                    ← 不含 query（/v1/messages?beta=true 签 /v1/messages）
{ts}                      ← 毫秒时间戳十进制字符串
{nonce}                   ← 12 字节随机，base64url
{deviceId}
{clientVersion}           ← 如 "0.0.322"，参与签名，过旧会被判 outdated
sha256hex(credential)     ← /v1/device/session 用 access token；模型路由用 ticket
{meta 头哈希}             ← sha256hex("k1\0v1\0k2\0v2")；无 meta 头时该行留空（空行，不是空串哈希）
sha256hex(body)           ← body 为原始字节；无 body 时是空字节的哈希
```

用 Ed25519 私钥对 canonical string 的 UTF-8 字节签名，签名值 base64url。

明文签名头（仅 `/v1/device/session` 这样发）：

```
x-mirasim-device: {deviceId}
x-mirasim-ts: {ts}
x-mirasim-nonce: {nonce}
x-mirasim-sig: {signature base64url}
x-mirasim-client: {clientVersion}
```

## mrs-seal-v1 密封（模型路由只接受密封形态，明文签名头会被拒）

把 4 个签名头（device/ts/nonce/sig）的 JSON 对象密封进 `x-mirasim-enc`：

```
shared  = X25519(临时私钥, relay 接收公钥)
key     = HKDF-SHA256(ikm=shared, salt=临时公钥(32字节), info="mrs-seal-v1", L=32)
aad     = "mrs-seal-v1\n{METHOD}\n{PATH}"        ← 绑定方法与路径
ct,tag  = ChaCha20-Poly1305(key, nonce(12字节随机), plaintext, aad)
输出    = 临时公钥(32) || nonce(12) || 密文 || tag(16)   ← tag 在最后
x-mirasim-enc = base64url(输出)
```

- plaintext = `JSON.stringify({"x-mirasim-device":..,"x-mirasim-ts":..,"x-mirasim-nonce":..,"x-mirasim-sig":..})`
- relay 接收公钥（X25519，base64，可用 `MIRASIM_SEAL_PUBKEY` 覆盖）：
  `HlyNMMeGXryasYLJuYQ/9ksCD4AYVVy1zXKAtJdpJn4=`

## 认证后端接口

| 接口 | 说明 |
|---|---|
| `GET {auth}/auth/oauth/providers` | 列出登录提供商（github / google） |
| `GET {auth}/auth/oauth/{provider}/login?redirect_uri=..&state=..` | 发起 OAuth；**redirect_uri 白名单只放行 loopback**（`http://127.0.0.1:任意端口/callback`），非 loopback 一律 400 |
| `POST {auth}/auth/code` `{email}` | 邮箱验证码登录：发码（开发环境可能回 `dev_code`） |
| `POST {auth}/auth/verify` `{email, code}` | 验码 → `{access_token, refresh_token}` |
| `POST {auth}/auth/refresh` `{refresh_token}` | 换 access token；**响应里的新 refresh_token 必须回写存储** |
| `POST {auth}/auth/license/preview` / `POST {auth}/auth/license/redeem` | 兑换码预览/兑换（需登录态） |

- 服务端签发的 OAuth `state` 只有 **10 分钟**有效期，且会把客户端传的 state 换成它自己签发的（回调带不回原 state，所以 loopback 自动回调用「独占端口」认领流程）。
- refresh token 是 JWT，payload 含 `email`、`plan`、`plan_exp`（秒级时间戳）、`token_type` 等，本地可解，无需请求。
- `/auth/me` 不接受 refresh token（401），不要用它校验。

## relay 侧接口与行为（逐条实测）

| 家族 | `/v1/messages` | `/v1/responses` | `/v1/chat/completions` |
|---|---|---|---|
| `claude-*` | ✅ | 400 | 400 |
| `gpt-*` | 未明确拒绝 | ✅（**仅流式**，stream 必须 true） | 404 |
| `kimi-k3` / `deepseek-*` / `glm-*` | ✅ | ✅ | ✅ |

- 模型清单：`GET /v1/models`；配额：`GET /v1/limits`（返回 `{windows:[{used,budget,...}], suspended,...}`）。
- 模型名写错 → 422 `model "xxx" is not supported at this time`。
- 模型容量打满 → 503 `model_capacity_exhausted`，**不计费、不是账号故障**；正确做法是同模型退避重试（默认 2 次，1.2s/2.4s 指数），用尽后如实透传 503。官方客户端会 fallback 到 `claude-opus-4-8`，本网关不换模型。
- 上游 5xx 是 relay 故障，不应熔断账号。
- prompt caching 有效且**按账号隔离**：Responses 链靠 `prompt_cache_key`，Messages 链靠 `cache_control:{type:'ephemeral'}`，chat 链自动缓存。缓存命中计入用量。

### `/v1/messages` 的隐含要求（不满足则 400 `the request was rejected as invalid`）

1. `system` 必须是顶层字段；混在 `messages` 里当 role 的要提到顶层并按原序合并。
2. 不允许 `top_p`（任何值都拒）——剔除。**只影响 /v1/messages**，chat completions 接受 top_p。
3. `claude-*` 模型要求 system 里出现 Claude Code 身份指纹（客户端鉴定）：
   `You are a Claude agent, built on Anthropic's Claude Agent SDK.`
   缺少一律 400。第三方模型（kimi/deepseek/glm）不查指纹，**不要**给它们注入。
4. **带指纹时 system 总长 ≤ 200 字节**（按字节数，与内容无关；不带指纹则不限）。
   报错文案 `system prompt must not present another product's instructions...` 是误导，触发因素是长度。
   伪装模式：`relaxed`（默认）= 指纹 + 原文，超 200 字节按字节截断（保头丢尾）；`strict` = 只发身份行。
5. `model` 的 `mirasim/` 前缀要剥离。

### `/v1/responses` 的形态要求

1. `input` 必须是消息数组；纯字符串 → 400。包装成 `[{"role":"user","content":[{"type":"input_text","text":...}]}]`。
2. Codex 模型 `stream` 必须为 true（relay 只提供流式）；网关不做转换，如实透传 relay 报错。
3. 不校验指纹；`role:'developer'` 的系统消息内容不校验。

## 选号与配额经验（来自参考实现）

- 定期拉 `/v1/limits`，取每个账号**最紧张窗口**的已用比例 `max(used/budget)`；剩余越多权重越高，按权重随机选取（保底权重 0.05）。
- **会话粘性**：会话键 = 客户端 `prompt_cache_key`，否则 `sha256(model + system + 首条消息)`；同会话优先复用上次的账号（缓存命中），失效自动回退。映射 TTL 10 分钟，上限 1000 条 LRU。
- 出错账号指数退避冷却（15s 起，2 倍递增，上限 10 分钟）；`suspended` / 额度耗尽（ratio ≥ 1）自动降温；401 换账号重试一次；5xx 不熔断。
- `/v1/limits` 快照保鲜期 10 分钟（避免上游调用翻倍）；后台每 10 分钟轮询一轮。
