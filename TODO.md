# mirasim2api 进度（2026-09-23 完成）

以 sub2api 产品架构为蓝本，上游适配 Mirasim。设计见 `docs/DESIGN.md`，协议见 `docs/PROTOCOL.md`。

**状态：项目已完成。** `go build ./...` / `go vet ./...` / `go test ./...` 全部通过，gofmt 全部已格式化。

## ✅ 全部完成

### 核心模块（internal/）
- [x] `config` / `mirasim`（mrs-sig-v2 签名 + mrs-seal-v1 密封 + token 链 + 设备身份）/ `store`（SQLite）
- [x] `pool`（额度加权选号 + 会话粘性 + 指数退避熔断 + 配额轮询）
- [x] `gateway`（/v1/messages、/v1/responses、/v1/chat/completions、/v1/models、/health + 裸路径）
- [x] `billing`（用量解析 + 计费批量落库）/ `runtimecfg`（热更设置）
- [x] **503 容量重试 / 401 换号重试 / 5xx 不熔断 / SSE tee 取 usage 已逐行核对落实**（forward.go）

### 本轮接管新增 / 修复
- [x] `internal/admin/settings.go` — 补齐 handleSettingsGet / handleSettingsPut（白名单键校验）
- [x] 修复 admin.go:63 编译错误 — `d.Settings()` 误调用 → `.Get()`
- [x] `cmd/server/main.go` — 装配全部模块 + 安全闸（无密码绑非 loopback 拒启）+ 优雅停机 + limits 轮询
- [x] `web/` — embed FS + 单页后台（登录/仪表盘/账号池/Keys/用量/设置 5 标签页，原生 JS）
- [x] **修复 validate() 语义反了导致 Key 创建/编辑恒 400 空错误**（handler 把 ok 当 bad）
- [x] `internal/admin/admin_test.go` — 回归测试：Key 创建+明文一次性+非法并发 400、settings 读写、未登录 401
- [x] `deploy/` — Dockerfile（多阶段静态编译 + 非 root + healthcheck）、docker-compose.yml、.env.example
- [x] `README.md` — 快速开始 / API 用法 / 配置表 / 项目结构 / 安全 / 免责声明
- [x] `.gitignore` + `.dockerignore`

### 冒烟测试通过项
启动、/health、/v1/models 鉴权(无 key 401 / 合法 key 200)、登录、summary、
Key 创建(201)+明文一次性+非法并发(400 中文消息)、模型白名单(403)、
WebUI 三资源(200)、失败请求经 billing 批量落库(500ms flush)、非 loopback 无密码拒启。

## ⏳ 仅余（需真实环境，代码层面无法再推进）
- [ ] 真实 Mirasim 账号联调：OAuth/邮箱加号 → 额度拉取 → 实际模型转发 + SSE 流式 + usage 解析 + 计费。
  本地仅能用空账号池验证路由/鉴权/静态资源/失败路径。
- [ ] Docker 镜像实机构建（本机无 docker；Dockerfile 为标准多阶段纯 Go 静态编译，路径可靠）。
- [ ] `git init` + 首次提交（用户按需自行执行）。

## ⚠️ 注意事项
- **MASTER_KEY** 丢则 refresh token 与设备私钥全部解不开，请显式备份。
- 免密模式（未设 ADMIN_PASSWORD）仅限本机 loopback；对外暴露必须先设密码。
- 计费为异步批量落库（500ms 或满 20 条 flush），用量日志查询可能滞后最多 500ms。
