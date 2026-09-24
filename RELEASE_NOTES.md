# Mirasim Workbench v1.0.2

Windows x64 本地桌面工作台，首次运行不包含任何账号或 API Key。

- 深色独立 WebView2 窗口、网关启动/停止/重启、账号池与 API Key 管理。
- GitHub / Google OAuth、邮箱码和手动 Token 导入；修复回调连接提前关闭，支持回调恢复和片段回调。
- 配额窗口的点数、百分比、剩余量与重置时间。
- Go 套餐目录：`kimi-k3`、`deepseek-flash`、`glm-5.3-flash`。
- 流式调试、用量记录、本地日志及官方客户端快捷入口。

## 下载与运行

下载 `mirasim-workbench-v1.0.2-windows-amd64.zip`，解压到可写目录，双击 `mirasim-workbench.exe` 或 `start-workbench.vbs`。系统需要 Windows x64 和 WebView2 Runtime；运行包不需要 Go 或 Node。

工作台默认 `127.0.0.1:7901`，模型网关默认 `127.0.0.1:8787/v1`。首次使用需自行授权并创建客户端密钥。

源码与第三方许可说明见仓库文档。上游未声明开源许可证，不将源码可见视为任意商用或再分发授权。使用前请核实权利人授权与服务条款。
