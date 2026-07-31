# Grok Build Switch

Grok Build Switch 是面向 macOS 的本地菜单栏/桌面工具，用于管理 Grok CLI 的 `~/.grok/config.toml`、供应商 Profile 与统一模型路由。

> 本仓库基于 [1parado/grok-build-switch](https://github.com/1parado/grok-build-switch) 开发，并集成 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 提供订阅账号代理能力。感谢原项目及相关开源项目的工作。

## 当前产品能力

- **官方 Grok CLI**：使用 Grok CLI 官方登录流程，并在登录后应用官方模型路由
- **普通 Profile**：管理供应商名称、Base URL、API Key、上游格式和模型
- **单一启用供应商路由**：官方或自定义供应商互斥启用；每个供应商记忆自己的 `default`、`web_search`、`subagents.explore` 与 `subagents.plan`，自定义切换保留组合模型目录以兼容旧会话别名
- **订阅代理**：通过内嵌 CLIProxyAPI 接入和管理受支持的第三方订阅
- **配置编辑**：查看、校验并编辑 `~/.grok/config.toml`
- **远程访问**：支持 LAN 配对访问和 SSH 连接管理
- **桌面入口**：提供 macOS 菜单栏与 Wails 桌面窗口

## 产品边界

上述清单是当前产品的完整功能范围；不在其中的旧扩展已从当前产品移除，不再作为可用功能、接口或数据目录出现在现行说明中。

用户已授权应用清理这些已移除能力遗留在自身 DataDir 中的旧记录。该授权不包含 Grok CLI 官方认证数据，也不包含订阅代理保存的账号或凭据；应用不得将两者作为旧记录删除。

## 系统要求

| 项目 | 说明 |
|------|------|
| 系统 | **macOS 15+**（仅 Apple Silicon / arm64） |
| 运行 | 双击 `Grok Build Switch.app` 即可 |
| 可选 | 本机已安装 [Grok CLI](https://x.ai)，配置目录默认为 `~/.grok` |

## 安装与使用

### 当前方式：从源码构建

当前仓库尚无可下载的 Release 资产，现阶段请从源码构建。后续发布后，可从 [Releases](../../releases) 页面下载 `Grok-Build-Switch-*.dmg`，拖入 Applications 文件夹运行。

```bash
./build-macos.sh
```

构建脚本仅支持在 Apple Silicon Mac 上生成 `dist/macos/Grok Build Switch.app`、arm64 DMG 和对应的 `.sha256`；应用与内嵌 CLIProxyAPI 最低支持 macOS 15。当前不提供 Intel 构建。默认 bundle marketing version 为 `0.0.0`、build version 为 `1`；发布构建可通过 `MARKETING_VERSION=1.2.3 BUILD_VERSION=42` 覆盖，两者必须符合 Apple 的纯数字点分格式。兼容旧调用时，`VERSION` 仍可作为 marketing version 输入。

Developer ID 签名：

```bash
APPLE_SIGNING_IDENTITY="Developer ID Application: Example (TEAMID)" \
  ./build-macos.sh --require-signature
```

## 文档

- [产品说明](docs/product.md)
- [使用教程](docs/usage.md)
- [当前状态](docs/status.md)
- [维护者说明](docs/agent.md)

在线文档站：[https://1parado.github.io/grok-build-switch/](https://1parado.github.io/grok-build-switch/)

## 数据与安全

- `~/.grok/config.toml` 是 Grok CLI 当前生效配置。
- 应用 DataDir 保存普通 Profile、统一路由、应用设置、LAN/SSH 配置和订阅代理运行数据。
- Profile 中的 API Key 以及订阅代理账号凭据属于敏感数据，不应提交到 Git。
- Grok CLI 官方认证由官方登录流程管理；清理应用旧记录时不得删除或改写该认证。
- 订阅代理凭据属于保留功能的数据；清理应用旧记录时不得删除。

## License

[MIT](./LICENSE)
