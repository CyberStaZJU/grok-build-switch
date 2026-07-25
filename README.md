# Grok Build Switch

macOS 托盘工具：用供应商（Profile）管理 Grok CLI 的 `~/.grok/config.toml`。

一键切换上游 `base_url`、默认模型、联网搜索模型、subagents 配置。

> 本仓库基于 [1parado/grok-build-switch](https://github.com/1parado/grok-build-switch) 开发，并集成 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 提供订阅账号代理能力。感谢原项目及相关开源项目的工作。

## 核心功能

- **多供应商管理**：增删改查供应商，支持名称、Base URL、API Key、上游格式、默认/联网/子代理模型
- **模型路由引擎**：将多个 Profile 投影为统一路由表，支持 `default`、`web_search`、`subagents.explore`、`subagents.plan` 四个路由维度
- **会话图谱**：跨供应商的逻辑会话与分支管理，可视化对话历史
- **对话整理**：AI 分析会话主题、建议标题、标记可删除的一次性对话
- **Browser-use 注入**：为非 Grok 模型自动注入 web_search / web_fetch 工具
- **订阅代理集成**：内嵌 CLIProxyAPI 管理第三方订阅账号
- **CodeBuddy 集成**：将本地 CodeBuddy CLI 暴露为 OpenAI 兼容推理端点
- **AI 对话工作台**：流式回复、工具权限、历史会话续接

## 系统要求

| 项目 | 说明 |
|------|------|
| 系统 | **macOS 14+** (Apple Silicon / Intel) |
| 运行 | 双击 `Grok Build Switch.app` 即可 |
| 可选 | 本机已安装 [Grok CLI](https://x.ai)，配置目录默认为 `~/.grok` |

## 安装与使用

### 方式一：从 Release 下载（推荐）

1. 打开 [Releases](../../releases) 页面
2. 下载 `Grok-Build-Switch-*.dmg`
3. 拖入 Applications 文件夹运行

### 方式二：从源码构建

```bash
./build-macos.sh
```

默认生成 `dist/macos/Grok Build Switch.app`、arm64 DMG 和对应的 `.sha256`。

Developer ID 签名：

```bash
APPLE_SIGNING_IDENTITY="Developer ID Application: Example (TEAMID)" \
  ./build-macos.sh --require-signature
```

## 文档

详细产品文档见 [**docs/product.md**](docs/product.md)，包含：
- 产品架构与模块职责
- 数据持久化路径
- HTTP API 路由总览
- 模型切换机制详解
- 构建与运行方式
- 常见问题定位指引

在线文档站：[https://1parado.github.io/grok-build-switch/](https://1parado.github.io/grok-build-switch/)

## 数据与安全

| 路径 | 内容 |
|------|------|
| `~/.grok/config.toml` | Grok CLI 当前生效配置 |
| `~/Library/Application Support/Grok Build Switch/profiles.json` | 供应商档案（含 API Key 明文） |
| `~/Library/Application Support/Grok Build Switch/backups/` | config 自动备份 |
| `~/Library/Application Support/Grok Build Switch/settings.json` | 本工具设置 |

## License

[MIT](./LICENSE)
