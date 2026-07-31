# 使用教程

## 1. 准备 Grok CLI

从 [Grok Build](https://grok.com/build) 安装 Grok CLI。需要使用官方账号时，请通过 Grok CLI 官方登录流程完成登录；Grok Build Switch 不托管或替代官方认证。

## 2. 启动 Grok Build Switch

在 Apple Silicon Mac 上构建并打开 `Grok Build Switch.app`。应用可从 macOS 菜单栏、Wails 桌面窗口或本地浏览器管理页使用。

## 3. 使用官方 Grok CLI 路由

1. 在应用中选择官方 Grok CLI；
2. 如果尚未登录，按提示完成 Grok CLI 官方登录；
3. 登录完成后返回应用，再次应用官方路由；
4. 在终端运行 `grok` 验证当前模型和配置。

应用旧记录清理不会删除或改写 Grok CLI 官方认证。

## 4. 添加普通 Profile

1. 打开 Profile 管理；
2. 填写名称、Base URL、API Key 和上游格式；
3. 拉取或填写该上游可用模型；
4. 保存 Profile。

普通 Profile 用于基础连接和常用模型选择。

## 5. 配置统一模型路由

在模型路由页面分别选择：

- default；
- web_search；
- explore；
- plan。

保存后，应用会校验选择并更新 `~/.grok/config.toml`。如写入失败，应确认原配置仍可解析，并检查 Profile、模型和文件权限。

## 6. 使用订阅代理

1. 打开订阅代理管理；
2. 按受支持流程登录订阅账号；
3. 确认代理状态正常；
4. 在统一模型路由中选择代理提供的模型。

订阅代理账号、令牌和凭据是保留数据，不属于已移除能力的旧记录清理范围。

## 7. 编辑 `config.toml`

应用支持查看、校验和编辑 Grok CLI 的 `~/.grok/config.toml`。编辑前确认内容属于当前环境，保存后可在终端运行 `grok` 验证。

## 8. 使用 LAN 与 SSH

- **LAN**：仅在可信网络中开启，完成配对后再访问管理页；
- **SSH**：添加连接信息后执行受支持的远程文件操作；
- 不要在截图、日志或公开 Issue 中暴露 API Key、官方认证或订阅代理凭据。

## 9. 使用菜单栏与 Wails

- 通过 macOS 菜单栏快速打开应用、查看状态和执行常用切换；
- 使用 Wails 窗口完成 Profile、路由、订阅代理、配置、LAN 和 SSH 管理。

## 10. 产品范围

本教程第 3 至第 9 节覆盖当前完整产品范围。其他旧扩展已移除，不再提供现行操作入口。
