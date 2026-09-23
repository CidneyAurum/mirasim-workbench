# mirasim2api

以 [sub2api](https://github.com/Wei-Shaw/sub2api) 的产品架构为蓝本（多账号池、API Key 分发、
精确计费、智能调度、并发控制、管理后台），上游适配 **Mirasim** 订阅额度的模型网关。

单一 Go 二进制交付，管理后台内嵌，SQLite 持久化，无外部中间件依赖。

> ⚠️ **免责声明**：本项目仅供技术学习与研究。使用可能违反 Mirasim 及上游（Anthropic/OpenAI 等）
> 的服务条款，风险由使用者自负；不得用于商业运营。

---

## 功能特性

- **多账号池**：GitHub/Google OAuth（loopback 自动回调或粘贴回调链接）+ 邮箱验证码 + 直接粘贴 refresh token 三种加号方式；refresh token AES-256-GCM 加密落盘，轮换自动回写
- **API Key 分发**：`sk-` 密钥（只存 sha256），每 Key 可配并发上限 / 每分钟请求数 / 模型白名单 / 过期时间
- **精确计费**：从响应（含 SSE 流末帧）解析 token 用量，模型价格表 × 倍率计费，写用量日志并累加到 Key
- **智能调度**：额度加权随机选号 + 会话粘性（prompt cache 友好）+ 出错指数退避熔断
- **并发与限流**：每账号并发信号量 + 每 Key 并发槽 + 每 Key 滑动窗口限流
- **协议兼容**：实现 Mirasim 的 mrs-sig-v2 签名与 mrs-seal-v1 密封，见 `docs/PROTOCOL.md`
- **管理后台**：内嵌单页 WebUI，仪表盘 / 账号池 / API Keys / 用量日志 / 设置，无需前端工具链

## 支持的端点

| 端点 | 模型家族 |
|---|---|
| `POST /v1/messages` | `claude-*`、`kimi-k3`、`deepseek-*`、`glm-*` |
| `POST /v1/responses` | `gpt-*`（仅流式）、第三方模型 |
| `POST /v1/chat/completions` | `kimi-k3`、`deepseek-*`、`glm-*` |
| `GET /v1/models` | 模型清单 |
| `GET /health` | 健康检查（账号池摘要） |

> 模型家族路由遵循 Mirasim relay 实测约束：`claude-*` 仅 `/v1/messages`，`gpt-*` 仅 `/v1/responses`，
> 第三方模型三个端点皆可。详见 `docs/PROTOCOL.md`。

同时注册裸路径（`POST /messages` 等，无 `/v1` 前缀），方便不改 base_url 的客户端。

---

## 快速开始

### 方式一：本地运行

需要 Go 1.27+。

```bash
go build -o mirasim2api ./cmd/server
ADMIN_PASSWORD=your-strong-password ./mirasim2api
```

打开 `http://127.0.0.1:8787/admin/`，用上面设置的密码登录。

### 方式二：Docker Compose

```bash
cd deploy
cp .env.example .env
# 编辑 .env：至少设置 ADMIN_PASSWORD 与 MASTER_KEY
docker compose up -d --build
```

数据持久化在名为 `mirasim-data` 的 volume（`/app/data`）。

### 添加账号

管理后台 →「账号池」→ 右上角三种方式任选：

- **OAuth 登录**：选 GitHub/Google，自动在本机 127.0.0.1 起临时端口接收回调（推荐，全自动）；
  或选手动模式，授权后把浏览器地址栏的完整回调链接粘贴回来
- **邮箱验证码**：输入邮箱收码，填码即入池
- **粘贴 Token**：直接粘贴一个有效的 refresh token（三段 JWT）

### 创建 API Key

管理后台 →「API Keys」→「新建 Key」，可设并发 / RPM / 模型白名单 / 过期时间。
**明文密钥只在创建时显示一次**，请立即保存。

### 调用模型

```bash
curl http://127.0.0.1:8787/v1/messages \
  -H "Authorization: Bearer sk-你的key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-5",
    "max_tokens": 256,
    "messages": [{"role":"user","content":"你好"}]
  }'
```

流式：`"stream": true`，网关逐行透传 SSE 并从流中解析用量计费。

---

## 配置（环境变量）

| 变量 | 默认 | 说明 |
|---|---|---|
| `PORT` / `HOST` | `8787` / `0.0.0.0` | 监听地址 |
| `DATA_DIR` | `./data` | SQLite 与 master.key 目录 |
| `MASTER_KEY` | 自动生成存 `DATA_DIR/master.key`（0600） | 64 位 hex，加密 refresh token 与设备私钥；**丢失则已存凭据解不开** |
| `ADMIN_PASSWORD` | 空 | 管理后台密码；空且绑定非 loopback 时**拒绝启动** |
| `GATEWAY_KEY_REQUIRED` | `true` | 模型请求是否必须带 sk- key |
| `CLAUDE_CLOAK_MODE` | `relaxed` | `relaxed` / `strict` |
| `CAPACITY_RETRIES` / `CAPACITY_BACKOFF_MS` | `2` / `1200` | 503 容量重试次数 / 退避毫秒 |
| `ACCOUNT_MAX_CONCURRENCY` | `5` | 每账号上游并发槽上限 |
| `UPSTREAM_PROXY` | 空 | 出站 HTTP CONNECT 代理 |
| `RELAY_URL` / `AUTH_URL` | 官方地址 | 上游覆盖 |
| `MIRASIM_CLIENT_VERSION` | `0.0.322` | 参与签名的客户端版本 |
| `MIRASIM_SEAL_PUBKEY` | 内置值 | relay 密封公钥 |

标记「可热更」的设置（代理 / 伪装模式 / 重试参数 / 价格表 / 倍率）也可在管理后台「设置」页在线修改，
数据库值优先于环境变量。

---

## 项目结构

```
cmd/server/          入口：配置加载、DB 初始化、HTTP 服务装配
internal/config/     环境变量配置
internal/mirasim/    协议层：设备身份、mrs-sig-v2 签名、mrs-seal-v1 密封、token 刷新、ticket 申领
internal/store/      SQLite：accounts / api_keys / usage_logs / settings / device
internal/pool/       账号池：额度加权选号、会话粘性、熔断冷却、配额轮询
internal/gateway/    模型网关：请求规范化、鉴权、并发限流、转发、SSE 透传、用量解析
internal/billing/    用量记录与费用计算
internal/admin/      管理 API（会话认证）
internal/runtimecfg/ 运行时设置（数据库优先，环境变量兜底）
web/                 管理后台静态资源（embed）
deploy/              Dockerfile、docker-compose.yml、.env.example
docs/                DESIGN.md（设计）、PROTOCOL.md（协议规格）
```

## 安全说明

- refresh token 与设备私钥一律 AES-256-GCM 加密落盘
- API Key 只存 sha256，展示用 `sk-...abcd` 截断形式
- 管理会话用 HttpOnly Cookie（HMAC 签名 token，8 小时有效）
- 未设 `ADMIN_PASSWORD` 且绑定非 loopback 地址时拒绝启动

## 测试

```bash
go test ./...
```

各协议、规范化、池选号、计费、存储模块均有单元测试。
