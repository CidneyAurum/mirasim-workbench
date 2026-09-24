# Mirasim Workbench

Windows x64 本地可视化工作台，基于 [Essaim8/mirasim2api](https://github.com/Essaim8/mirasim2api)。
深色独立窗口、账号池、API Key、周额度、流式调试与网关管理，解压后双击启动。

> 发布包不包含任何账号、令牌、数据库、日志或个人配置。上游未声明开源许可证，使用与再分发前请阅读 [来源与许可说明](THIRD_PARTY_NOTICES.md)。

## 在自己的电脑上部署

1. 从 [Releases](https://github.com/CidneyAurum/mirasim-workbench/releases) 下载 `mirasim-workbench-v1.0.2-windows-amd64.zip`。
2. 完整解压到当前用户有写入权限的目录。不要在 ZIP 预览中直接运行，也不要放在需要管理员权限的系统目录。
3. 双击 `mirasim-workbench.exe`，或 `start-workbench.vbs`。
4. 在「账号池」登录自己的 Mirasim 账号，在「API Keys」创建并保存访问密钥。
5. 使用「调试台」测试，或把下方地址配置到客户端。

运行包**不需要安装 Go、Node、Python、Docker 或数据库**。需要 Windows 10/11 x64 与 [Microsoft WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/)。WebView2 缺失时工作台会尝试使用默认浏览器打开本地页面。

官方 Mirasim 客户端不是网关运行的必需依赖；如果已安装，工作台会显示打开入口，不会读取其登录凭据。

| 配置 | 默认值 |
|---|---|
| 工作台 | `http://127.0.0.1:7901` |
| 客户端 Base URL | `http://127.0.0.1:8787/v1` |
| Kimi / DeepSeek / GLM 接口 | `/v1/chat/completions` |
| Go 套餐模型 | `kimi-k3`、`deepseek-flash`、`glm-5.3-flash` |
| API Key | 由使用者在本机工作台创建 |

Go 套餐模型目录不混入 Claude、GPT 或 DeepSeek V4。其他 / 混合套餐由上游实际权限决定，通用参考清单不等于全部已授权。

## 功能

- GitHub / Google 浏览器授权、邮箱验证码、Refresh Token 与完整回调恢复。
- 配额窗口：已用 / 总点数、百分比、剩余量、重置时间与数据更新时间。
- 账号启停、额度刷新；API Key 并发、RPM、模型白名单和到期时间。
- Chat Completions / Messages / Responses 流式调试，按模型家族选择协议。
- 用量与计费记录、运行日志、本地网关启停和重启。
- 仅监听回环地址；账号加密存储，管理密码由 Windows DPAPI 保护。

已修复上游 OAuth 回调过早断开连接的问题，授权后可正常显示成功页面，并清除地址栏中的令牌。

## 日常使用

关闭桌面窗口会保留后台网关。通过「设置与日志 → 停止网关」或 `stop-workbench.vbs` 停止服务。启动 / 重启不会清空账号。

数据在安装目录下的 `data/`，日志在 `logs/`。首次启动会为当前 Windows 用户独立生成凭据，**不要把自己运行过的安装目录打包分享**。备份时先停止网关，然后完整备份数据库及 `master.key`。详情见 [使用说明](WORKBENCH.md) 和 [安全说明](SECURITY.md)。

端口已被占用时，可在终端指定另一组本机端口：

```powershell
.\mirasim-workbench.exe -addr 127.0.0.1:7902 -gateway-port 8788
```

使用自定义端口时，请在对应工作台页面中停止服务；默认停止脚本仅针对 7901 端口。

## 源码构建

需要 Go 1.27.1+。Node 仅用于可选的前端单元测试，不是运行依赖。

```powershell
.\tools\build.ps1 -GoPath '完整路径\go.exe'
.\tools\create-shortcut.ps1
```

构建到其他目录：添加 `-OutputDirectory '输出目录'`。编译使用 `-trimpath -buildvcs=false`，移除本机源码路径和工作区版本元数据。

运行测试：

```powershell
go test ./...
go vet ./...
Push-Location workbench
go test ./...
node --test web/protocol.test.mjs web/quota.test.mjs
Pop-Location
```

打包为全新、无账号状态的源码及 Windows ZIP：

```powershell
.\tools\package.ps1 -GoPath '完整路径\go.exe'
```

脚本只从源码白名单导出，不复制运行目录数据；重新编译程序，并生成 ZIP、SHA-256 和脱敏检查报告。

## 来源和限制

上游基准：`a6181c9f2f3d0f51c91c12f3163a872113eb304f`，原始文档保留在 [UPSTREAM_README](docs/UPSTREAM_README.md)。
详见 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES.md)。

仅供技术学习研究。使用可能违反 Mirasim 或模型提供方的服务条款，不承诺模型可用性或配额。本项目未添加重新许可上游代码的开源许可证，也不构成商用或再分发授权。公开仓库并不改变这一点。
