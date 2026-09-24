# 移植与部署指南

这份指南面向三类使用者：

1. **普通用户**：只想在自己的 Windows 电脑上运行，不编译源码。
2. **Windows 开发者**：从源码构建或二次开发。
3. **Linux / Docker 用户**：把网关核心部署到服务器或 NAS。

所有方式都**不包含任何账号、令牌或数据库**。首次运行后会生成属于你自己电脑的数据目录，请勿把运行过的 data/、logs/、auths/（如有）分享或提交。

---

## 方式一：Windows 普通用户（推荐，零依赖）

### 系统要求

| 项目 | 要求 |
|---|---|
| 操作系统 | Windows 10 / 11 x64 |
| WebView2 | 需要安装（Win11 一般自带） |
| 其他依赖 | 无需 Go、Node、Python、Docker、数据库 |

WebView2 下载地址：<https://developer.microsoft.com/microsoft-edge/webview2/>。如果缺少 WebView2，工作台会自动回退到默认浏览器打开本地页面。

### 步骤

1. 从 [Releases](https://github.com/CidneyAurum/mirasim-workbench/releases) 下载 `mirasim-workbench-v*-windows-amd64.zip`。
2. 完整解压到当前用户有写入权限的目录，不要直接在 ZIP 预览里运行。推荐 `D:/Apps/MirasimWorkbench`，避免 `C:/Program Files`。
3. 双击 `mirasim-workbench.exe`，或 `start-workbench.vbs`。
4. 首次启动会自动生成 `data/`、加密密钥、随机管理密码，并在回环地址启动网关。
5. 在「账号池」点「添加账号」，用 GitHub / Google / 邮箱验证码 / Refresh Token 登录自己的 Mirasim 账号。
6. 在「API Keys」创建密钥，明文只显示一次，立即复制保存。
7. 客户端配置：

| 配置 | 值 |
|---|---|
| Base URL | `http://127.0.0.1:8787/v1` |
| API Key | 你在工作台创建的 `sk-...` |

### 端口冲突处理

默认端口：工作台 `7901`，网关 `8787`。如果被占用：

```powershell
.\mirasim-workbench.exe -addr 127.0.0.1:7902 -gateway-port 8788
```

之后所有客户端的 Base URL 改为 `http://127.0.0.1:8788/v1`。

### 停止与卸载

关闭窗口后网关仍在后台运行。停止方法：工作台内「设置与日志 → 停止网关」，或双击 `stop-workbench.vbs`。

完整卸载：停止服务后直接删除整个安装目录。没有注册表项、没有系统服务、没有全局配置文件。

---

## 方式二：从源码构建（Windows）

### 前置

- Go 1.27.1 或更高：<https://go.dev/dl/>
- WebView2 Runtime（运行时需要，编译时不需要）
- Node.js 仅在你要跑前端单元测试时才需要

### 步骤

```powershell
# 1. 克隆仓库
git clone https://github.com/CidneyAurum/mirasim-workbench.git
cd mirasim-workbench

# 2. 构建（Go 在 PATH 时）
.\tools\build.ps1

# 如果 Go 不在 PATH：
.\tools\build.ps1 -GoPath 'C:/Program Files/Go/bin/go.exe'

# 3. 可选：创建桌面快捷方式
.\tools\create-shortcut.ps1
```

构建产物：

- `mirasim2api.exe`：网关核心（命令行）
- `mirasim-workbench.exe`：桌面工作台（含内嵌网关管理）

编译使用 `-trimpath -buildvcs=false`，不包含本机源码路径。构建到其他目录：`-OutputDirectory '输出目录'`。

### 运行测试

```powershell
go test ./...
go vet ./...

Push-Location workbench
go test ./...
# 可选前端测试（需要 Node）
node --test web/protocol.test.mjs web/quota.test.mjs
Pop-Location
```

---

## 方式三：只部署网关核心（Linux / Docker）

工作台是 Windows 专用的，但网关核心是跨平台的 Go 代码。服务器 / NAS / Linux 用户可以直接部署 `cmd/server`。

### Docker Compose（推荐）

```bash
cd deploy
cp .env.example .env
# 编辑 .env：至少设置 ADMIN_PASSWORD 和 MASTER_KEY
docker compose up -d --build
```

数据持久化在 Docker 卷 `mirasim-data`（`/app/data`）。

### 直接运行

```bash
go build -o mirasim2api ./cmd/server
export HOST=0.0.0.0
export PORT=8787
export ADMIN_PASSWORD='你的强密码'
export DATA_DIR='/var/lib/mirasim2api'
./mirasim2api
```

打开 `http://<主机>:8787/admin/`，用 `ADMIN_PASSWORD` 登录后添加账号和创建 API Key。

### Docker 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `ADMIN_PASSWORD` | 空 | 管理后台密码；对外部署必须设置 |
| `MASTER_KEY` | 自动生成 | 32 字节 hex，加密凭据；丢失则数据无法恢复 |
| `HOST` / `PORT` | `0.0.0.0` / `8787` | 监听地址 |
| `DATA_DIR` | `/app/data` | SQLite 与密钥目录 |
| `GATEWAY_KEY_REQUIRED` | `true` | 模型请求是否必须携带 sk- 密钥 |
| `UPSTREAM_PROXY` | 空 | 出站 HTTP CONNECT 代理 |
| `RELAY_URL` / `AUTH_URL` | 官方地址 | 上游覆盖 |

完整变量见 [deploy/.env.example](deploy/.env.example)。

---

## 数据迁移

### 同一台电脑换目录

1. 停止网关（工作台「停止网关」或 `stop-workbench.vbs`）。
2. 完整复制整个安装目录到新位置（包含 `data/` 和 `logs/`）。
3. 双击新位置的 `mirasim-workbench.exe`。

### 跨电脑迁移（Windows → Windows）

1. 在旧电脑停止网关。
2. 完整备份 `data/` 目录，其中必须包含 SQLite 数据库与 `master.key`。
3. 在新电脑解压全新发布包，先运行一次并退出。
4. 停止新电脑上的网关。
5. 用旧电脑的 `data/` 覆盖新电脑的 `data/`。
6. 删除新电脑 `data/` 中的 `workbench-secret.dpapi`（如果存在）。这个文件绑定 Windows 用户，跨电脑无效；删除后工作台会重新生成本地管理密码，不影响账号数据。
7. 重新启动工作台。**不要删除 `master.key`**，否则所有加密账号无法解密。

### 数据位置

| 内容 | 位置 |
|---|---|
| 数据库、密钥 | `<安装目录>/data/` |
| 运行日志 | `<安装目录>/logs/` |
| 管理密码密文 | `data/workbench-secret.dpapi` |
| 账号加密密钥 | `data/master.key` |

---

## 常见问题

### 双击打不开 / 闪退

1. 用终端运行看报错：`./mirasim-workbench.exe`。
2. 检查是否完整解压了 ZIP（不是只双击了里面的 exe）。
3. 检查目录是否有写入权限。
4. WebView2 缺失时回退到浏览器打开，这不是错误。

### 端口被占用

用 `-addr` 和 `-gateway-port` 换端口，见上文「端口冲突处理」。

### 换电脑后账号不见了 / 登录不上

必须把旧电脑的 `data/` 整个目录（含 `master.key`）一起迁过来，并删掉 `workbench-secret.dpapi`。只复制 `*.db` 而不复制 `master.key` 是最常见错误。

### Linux 上能用图形工作台吗？

不能。`workbench/` 依赖 Windows WebView2 和 DPAPI。Linux 用 Docker 或直接运行网关核心，然后用浏览器访问 `/admin/` 管理界面（上游自带）。

### 如何彻底清理？

停止服务后删除整个安装目录即可。没有全局数据残留。

---

## 安全须知

- 仅供个人学习与研究，不要用于商业运营。
- 使用可能违反 Mirasim 或模型提供方的服务条款，风险自负。
- 不要把运行过的 `data/` 目录、日志或账号文件公开分享。
- 管理接口默认只监听回环地址；如需对外暴露，务必设置强密码并了解风险。
- 上游项目未声明开源许可证，再分发前请阅读 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
