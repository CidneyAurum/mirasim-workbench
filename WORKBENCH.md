# Mirasim 本地工作台（Windows）

## 1.0.2 周额度与 Go 套餐

- 账号卡片按上游窗口显示已用 / 总点数、已用百分比、剩余点数、重置时间、数据更新时间。每周窗口优先展示，不再只显示最紧张窗口的汇总比例。
- 未读取、未返回和刷新失败明确标注；零总额按官方客户端含义展示为「不限额」，不伪造百分比。
- 网关启动后立即读取配额，随后按原有周期更新，也可点击「刷新额度」。
- 只有 Go 套餐账号时，模型目录、调试台候选及 `/v1/models` 仅提供 `kimi-k3`、`deepseek-flash`、`glm-5.3-flash`；不把 `deepseek-v4-flash`、Claude 或 GPT 列为 Go 可用模型。未知或混合套餐不推测其权限。

## 1.0.1 授权回调修复

- 修复上游在回调处理中先关闭 HTTP 连接、再发送成功页面的问题；账号虽然入池，Edge 却会显示连接失败。
- 成功页面可在授权有效期内重复刷新；重复回调不会再次导入或覆盖账号凭据。
- 支持查询参数及 `#fragment` 两种回调方式。页面接收后清除地址栏中的令牌；刷新空回调不会提前结束授权。
- 工作台新增始终可见的「从回调链接恢复账号」、重新打开授权页和授权重试；成功后自动进入账号池。
- 如果旧版回调页显示无法连接，请先看账号池。若账号已经存在且额度正常，不必再授权或粘贴旧令牌。

已用独立临时账号测试真实 TCP 回调、浏览器片段回调与入池状态；未使用用户真实令牌做测试。

解压到任意当前用户可写的独立目录使用，不依赖固定盘符。基于 [Essaim8/mirasim2api](https://github.com/Essaim8/mirasim2api)，上游版本 `a6181c9f2f3d0f51c91c12f3163a872113eb304f`。

保留上游账号池、协议签名、计费和 SQLite 核心，增加深色 WebView2 桌面窗口；不修改官方 Mirasim 客户端。

## 打开与使用

1. 双击桌面 **Mirasim Workbench**，或者本目录的 `start-workbench.vbs` / `mirasim-workbench.exe`。
2. 在「账号池 → 添加账号」登录你自己的 Mirasim 账号，支持 GitHub、Google、邮箱验证码、Refresh Token。
3. 在「API Keys」创建密钥，立即复制保存。也可以点「用于本窗口调试」。
4. 「调试台」默认选 `kimi-k3`；「模型目录」将 `kimi-k3` 与 `deepseek-flash` 放在前面。

客户端配置：

| 配置 | 值 |
|---|---|
| Base URL | `http://127.0.0.1:8787/v1` |
| API Key | 在工作台创建的 `sk-...` 密钥 |
| Go 套餐模型 | `kimi-k3` / `deepseek-flash` / `glm-5.3-flash` |
| Kimi / DeepSeek 默认接口 | `POST /v1/chat/completions` |

Kimi、DeepSeek、GLM 同时支持 Messages / Responses；Claude 仅用 Messages，GPT 仅用流式 Responses。调试台自动选择对应协议，**不会偷偷换模型**。

模型列表来自上游内置清单或网关 `/v1/models`。网关在无账号或上游不可用时也可能返回内置清单；这不是账号授权或模型调用成功的证明。实际可用性取决于你的订阅、网络和上游容量。

## 已安装的官方客户端

工作台检测官方客户端默认安装位置 `%LOCALAPPDATA%\Programs\@mirasimdesktop\Mirasim.exe`。「设置与日志 → 打开官方 Mirasim」可直接启动已安装的客户端，不重复安装。

官方客户端的登录状态**不会自动导入**反代。工作台不会扫描客户端数据库、提取令牌或改写其设置。请通过工作台的登录入口授权；已有 GitHub / Google 浏览器登录可由你自行选择使用。

## 启动、停止与本地安全

- 工作台：`http://127.0.0.1:7901`；网关：`http://127.0.0.1:8787`。都只监听回环地址。
- 打开工作台会启动网关；关闭窗口保留网关运行。「设置 → 停止网关」停止模型服务。`stop-workbench.vbs` 停止默认端口下的网关和主工作台进程。
- 重启和停止会中断进行中的请求。只操作本部署记录的 PID，并校验完整可执行文件路径与进程创建时间；不按同名程序批量结束。
- 管理密码随机生成，由 Windows DPAPI 加密，仅供工作台内部认证；无需每次输入管理密码。上游 `/admin/` 后台仍受密码保护，日常管理请使用工作台。
- 数据目录设置当前 Windows 用户及 SYSTEM 的 ACL；账号加密沿用上游的 AES-256-GCM。API Key 列表仅保存散列和截断展示，明文只在创建响应中出现一次。
- 调试 Key 只保留在当前窗口内存，不保存到 localStorage。刷新页面或关闭窗口后需要重新填写。
- 本地管理 API 校验 Host、Origin 和专用请求头，阻止普通外部网页操作本机服务；不为公网部署设计。
- 未设置代理时直连。如需网络代理，在「设置」填写你自己使用的 HTTP CONNECT 代理地址，例如 `http://127.0.0.1:7890`，保存即生效。
- 本工作台不会启动登录、消费订阅额度或调用模型，除非你执行相应操作。

## 数据与备份

先停止网关，再完整备份 `data` 目录；**SQLite 数据库和 `master.key` 必须一起保留**。`workbench-secret.dpapi` 绑定当前 Windows 用户；跨电脑迁移时先备份，再移走此单个文件以重新生成本地管理密码，不要删除 `master.key`。不要公开分享 `data`。

日志位于 `logs/server.log` 与 `logs/workbench.log`。工作台页面只读最近 64 KB。停止服务后可按需归档日志。

## 重新构建与验证

需要 Go 1.27.1+；原生窗口依赖系统 WebView2 Runtime。运行：

```powershell
.\tools\build.ps1
.\tools\create-shortcut.ps1
```

如果 Go 不在 PATH，用 `-GoPath '完整路径\go.exe'`。用 `-OutputDirectory '输出目录'` 可避免覆盖正在运行的程序。脚本不会安装 Go 或系统依赖。

附加测试：

```powershell
cd workbench
$env:MIRASIM_TEST_BINARY=(Resolve-Path '..\mirasim2api.exe').Path
go test -v ./...
node --test web/protocol.test.mjs web/quota.test.mjs
```

集成测试在临时目录中启动独立网关，验证账号池为空时的真实错误、密钥生命周期、模型目录、设置持久化和进程管理，不读取实际账号、不会向真实模型发送请求。前端测试覆盖 Kimi / DeepSeek 路由、Claude / GPT 协议限制、分片 UTF-8 / CRLF / SSE 事件及错误处理。

上游本地补丁包括 `internal/config/config_test.go` 的 Windows 权限断言适配，以及 `internal/admin/oauth.go` 的回调生命周期、重复请求、片段回调与错误处理修复；模型转发协议未修改。实际 Windows 数据保护由工作台目录 ACL 完成。

遵循上游免责声明：仅供技术学习研究；使用前请确认相关服务条款和订阅授权。
