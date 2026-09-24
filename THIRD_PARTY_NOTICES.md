# 来源与许可说明

本项目的网关源码基于 [Essaim8/mirasim2api](https://github.com/Essaim8/mirasim2api)，上游基准提交为 `a6181c9f2f3d0f51c91c12f3163a872113eb304f`。新增 Windows 工作台及本地修复包括授权回调、配额展示和 Go 套餐模型目录。

截至本版本整理时，上游仓库**未提供 LICENSE 文件或声明开源许可证**。源码可见不等于授予任意复制、再分发或商用权利；本项目不擅自将上游代码重新许可为 MIT、Apache 或其他许可证。公开发行、再分发或商用前，请向相应权利人确认授权。此说明不构成法律意见。

保留上游免责声明：仅供技术学习研究，使用可能违反服务条款，请自行核实相关平台条款与订阅授权。

运行包使用 Go 标准库、go-webview2、go-winloader、golang.org/x/sys、golang.org/x/crypto、modernc.org/sqlite 及其依赖。打包脚本将从已解析的模块缓存中收集第三方 LICENSE / COPYING / NOTICE 文件，附于运行包 `third-party-licenses/`；适用条款以各依赖自己的许可文本为准。WebView2 Runtime 为 Microsoft 提供的独立系统组件，不随运行包分发。

上游协议中的密封公钥属于公开协议常量，不是私人凭据；测试中的固定密钥、签名、示例账号为测试数据。
