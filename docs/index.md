# Grok Build Switch

Grok Build Switch 是一个 macOS 本地菜单栏/桌面工具，用于管理 Grok CLI 官方登录路由、普通供应商 Profile、统一模型路由、`~/.grok/config.toml`，以及可选的 Grok Build Max Collaboration 配置预设。

## 快速入口

- [使用教程](usage.md)
- [产品说明](product.md)
- [当前状态](status.md)
- [Max Collaboration 设计](design/max-collaboration.md)
- [项目仓库](https://github.com/CyberStaZJU/grok-build-switch)
- [联系方式](contact.md)

## 当前能力

- 使用 Grok CLI 官方登录流程并切换官方模型路由
- 管理多个普通 Profile、Base URL、API Key 和模型
- 配置 default、web_search、explore 和 plan 统一路由
- 为主协调、任务拆解、主实现、困难实现 / 复核分别选择可信 Codex 订阅供应商内的 Standard 模型锚点、Standard/Fast 速度档与推理强度，预览并生成四角色 role/workflow，以 1/2/11/12/13 精确预算门控 Grok Build 调用，并由 workflow 顶层启动 10 个主实现 agent
- 查看 prompt、cached prompt、completion、reasoning token 和缓存命中率，不虚构美元成本
- 使用内嵌 CLIProxyAPI 管理订阅代理
- 查看、校验和编辑 `config.toml`
- 通过 LAN、SSH、macOS 菜单栏和 Wails 使用管理功能

## 当前产品边界

“当前能力”是完整产品范围；其他旧扩展已移除，不再作为当前功能、接口或数据目录列出。

用户已授权清理这些已移除能力留下的应用 DataDir 旧记录。该清理不得删除 Grok CLI 官方认证，也不得删除订阅代理账号或凭据。

## 安装

当前 macOS arm64 版本请按仓库 README 从源码构建；发布资产可用后再从项目 Releases 页面下载。
