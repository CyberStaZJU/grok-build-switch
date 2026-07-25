# Grok Build Switch 产品文档

> 本文档面向开发者和高级用户，提供 Grok Build Switch（以下简称 "GS"）的产品架构、模块职责、数据持久化、API 路由总览以及 debug 排查指引。目标是让读者能在不熟悉代码库的情况下，快速定位功能对应的源码模块。

---

## 目录

1. [产品定位](#1-产品定位)
2. [系统架构](#2-系统架构)
3. [目录结构](#3-目录结构)
4. [核心功能清单](#4-核心功能清单)
5. [internal/ 子模块职责说明](#5-internal-子模块职责说明)
6. [数据持久化路径](#6-数据持久化路径)
7. [HTTP API 路由总览](#7-http-api-路由总览)
8. [构建与运行方式](#8-构建与运行方式)
9. [常见问题定位指引](#9-常见问题定位指引)
10. [关键依赖](#10-关键依赖)

---

## 1. 产品定位

Grok Build Switch 是一个本地 macOS 托盘工具，用于管理 [Grok CLI](https://x.ai) 的 `~/.grok/config.toml` 配置。核心能力：

- **多供应商管理**：以 "Profile" 为单位管理不同上游 LLM 供应商（OpenAI 兼容接口），一键切换 `base_url`、默认模型、联网搜索模型、subagents 配置。
- **路由策略引擎**：将多个 Profile 投影为统一的多供应商路由表，支持 `default`、`web_search`、`subagents.explore`、`subagents.plan` 四个路由维度。
- **订阅代理集成**：内嵌 CLIProxyAPI 作为本地订阅账号代理，统一管理第三方订阅账号的认证、轮换与 token 刷新。
- **AI 对话工作台**：内置基于 ACP 协议的 Grok Agent 桥接器，提供流式对话、工具权限、历史会话续接能力。
- **CodeBuddy 集成**：将本地安装的 CodeBuddy CLI 暴露为 OpenAI 兼容的推理端点。
- **Grok 号池**：批量导入 Grok CLI `auth.json`，支持定时巡检、健康分类、坏号隔离与健康号轮换。
- **局域网访问**：可选开启 `0.0.0.0` 监听，支持手机扫码配对后远程管理。
- **会话图谱**：跨供应商的逻辑会话与分支管理。
- **对话整理**：AI 分析会话主题、建议标题、标记可删除的一次性对话。

---

## 2. 系统架构

```
┌─────────────────────────────────────────────────────────────┐
│                       用户界面层                              │
│  ┌──────────────┐  ┌──────────────┐                        │
│  │  Web UI       │  │  macOS 菜单栏 │                        │
│  │  (ui/app.js)  │  │  (gui_tray_  │                        │
│  │              │  │   darwin_)   │                        │
│  └──────┬───────┘  └──────┬───────┘                        │
│         │                 │                                 │
│         └─────────────────┘                                 │
│                           │ HTTP (127.0.0.1:17878)          │
├───────────────────────────┼─────────────────────────────────┤
│                      服务层  │ (internal/server)               │
│  ┌────────────────────────┴─────────────────────────────┐   │
│  │  Server (server.go)                                  │   │
│  │  ├─ 路由注册 (routes)                                 │   │
│  │  ├─ 路由策略引擎 (routing.go)                          │   │
│  │  ├─ 订阅代理桥接 (subscription_proxy.go)               │   │
│  │  ├─ 订阅推理转发 (subscription_inference.go)           │   │
│  │  ├─ Grok Agent 桥接 (agent.go)                        │   │
│  │  ├─ CodeBuddy 推理端 (codebuddy.go)                   │   │
│  │  ├─ 号池管理 (grokpool.go)                            │   │
│  │  ├─ 注册机 (registrar.go)                             │   │
│  │  ├─ CPA Mint (cpamint.go)                            │   │
│  │  ├─ 会话图谱 (session_graph.go)                       │   │
│  │  ├─ 缓存统计 (cache_stats.go)                         │   │
│  │  ├─ 浏览器使用策略 (browseruse_policy.go)              │   │
│  │  └─ 局域网访问控制 (lan_access.go)                     │   │
│  └──────────────────────────────────────────────────────┘   │
├─────────────────────────────────────────────────────────────┤
│                      核心逻辑层                               │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌──────────┐ │
│  │ switcher   │ │ profiles   │ │ routing    │ │ config   │ │
│  │ 配置切换    │ │ 供应商档案  │ │ 路由引擎   │ │ TOML IO  │ │
│  └────────────┘ └────────────┘ └────────────┘ └──────────┘ │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌──────────┐ │
│  │ agentbridge│ │ cliproxy   │ │ grokpool   │ │ codebuddy│ │
│  │ ACP 桥接   │ │ 订阅代理    │ │ 号池管理   │ │ CLI 运行 │ │
│  └────────────┘ └────────────┘ └────────────┘ └──────────┘ │
├─────────────────────────────────────────────────────────────┤
│                      基础设施层                               │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌──────────┐ │
│  │ paths      │ │ settings   │ │ autostart  │ │ crash    │ │
│  │ 路径解析    │ │ 设置存储    │ │ 开机自启   │ │ 崩溃恢复  │ │
│  └────────────┘ └────────────┘ └────────────┘ └──────────┘ │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌──────────┐ │
│  │ recovery   │ │ singleinst │ │ netproxy   │ │ notify   │ │
│  │ 损坏恢复    │ │ 单实例锁    │ │ HTTP 代理  │ │ 系统通知  │ │
│  └────────────┘ └────────────┘ └────────────┘ └──────────┘ │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌──────────┐ │
│  │ grokauth   │ │ remoteaccess│ │ ssh       │ │ browseruse│ │
│  │ Grok 认证   │ │ 局域网会话  │ │ SSH 远程  │ │ MCP 服务  │ │
│  └────────────┘ └────────────┘ └────────────┘ └──────────┘ │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐              │
│  │ registrar  │ │ cpamint    │ │ cachestats │              │
│  │ 账号注册机  │ │ CPA Mint   │ │ 缓存统计   │              │
│  └────────────┘ └────────────┘ └────────────┘              │
└─────────────────────────────────────────────────────────────┘
```

---

## 3. 目录结构

```
grok-build-switch/
├── main.go                          # 入口（!wailsgui build tag），初始化所有模块
├── gui_main.go                      # Wails GUI 入口（wailsgui build tag）
├── gui_frameworks_darwin.go         # macOS 菜单栏框架初始化
├── gui_options_darwin.go            # macOS 菜单栏菜单构建
├── gui_tray.go                      # 托盘应用抽象层
├── gui_tray_darwin.go               # macOS 菜单栏实现
├── gui_tray_darwin_provider.go      # macOS 菜单栏数据提供者（HTTP 客户端）
├── gui_tray_provider.go             # 托盘数据提供者接口
├── instance_url.go                  # 单实例 URL 发现逻辑
├── assets/                          # 应用图标资源
│   └── icon-macos.png
├── build-macos.sh                   # macOS 构建脚本
├── go.mod / go.sum                  # Go 模块定义
├── vendor/                          #  vendored 依赖（含 CLIProxyAPI 压缩包）
├── ui/                              # Web UI 前端（原生 HTML/JS）
│   ├── app.js                       # 主应用逻辑
│   ├── index.html                   # 主页面
│   ├── style.css                    # 样式
│   └── vendor/                      # 前端第三方库（highlight.js, marked, mermaid, katex）
├── docs/                            # 文档站（MkDocs）
│   ├── index.md / usage.md / plan.md / status.md / agent.md
│   └── product.md                   # 本文档
└── internal/                        # 核心业务逻辑（20+ 个子模块）
    ├── server/                      # HTTP 服务器与 API 处理器
    ├── switcher/                    # 配置切换引擎
    ├── profiles/                    # 供应商档案存储
    ├── routing/                     # 路由策略引擎
    ├── config/                      # TOML 配置读写
    ├── agentbridge/                 # Grok Agent ACP 桥接
    ├── cliproxy/                    # CLIProxyAPI 集成
    ├── grokpool/                    # Grok 号池管理
    ├── grokauth/                    # Grok 官方认证存储
    ├── codebuddy/                   # CodeBuddy CLI 运行器
    ├── registrar/                   # 账号自动注册机
    ├── cpamint/                     # CPA Mint 认证
    ├── browseruse/                  # 浏览器 MCP 服务
    ├── netproxy/                    # HTTP 代理传输层
    ├── notify/                      # 系统通知
    ├── autostart/                   # 开机自启
    ├── crash/                       # 崩溃恢复与日志
    ├── recovery/                    # 损坏文件恢复
    ├── singleinstance/              # 单实例锁
    ├── remoteaccess/                # 局域网会话管理
    ├── ssh/                         # SSH 远程连接
    ├── settings/                    # 应用设置存储
    ├── paths/                       # 跨平台路径解析
    ├── tray/                        # 托盘控制器
    └── cachestats/                  # 缓存命中率统计
```

---

## 4. 核心功能清单

| 功能 | 描述 | 关键模块 |
|------|------|----------|
| Profile CRUD | 增删改查供应商档案（名称、BaseURL、API Key、模型列表） | `profiles/`, `server/` |
| 配置切换 | 将 Profile 写入 `~/.grok/config.toml`，支持备份与还原 | `switcher/`, `config/` |
| 多供应商路由 | 将多个 Profile 投影为统一路由表，支持 default/web_search/subagents 路由 | `routing/`, `config/` |
| 官方账号切换 | 切换到 Grok CLI 官方认证（`grok login`） | `grokauth/`, `switcher/` |
| 订阅代理 | 内嵌 CLIProxyAPI 管理第三方订阅账号 | `cliproxy/`, `server/` |
| AI 对话 | 基于 ACP 协议的 Grok Agent 流式对话 | `agentbridge/`, `server/` |
| CodeBuddy | 将 CodeBuddy CLI 暴露为本地 OpenAI 兼容推理端点 | `codebuddy/`, `server/` |
| Grok 号池 | 批量导入 auth.json，定时巡检、健康分类、轮换 | `grokpool/`, `server/` |
| 账号注册 | 自动注册 Grok 账号（邮箱 + 浏览器自动化） | `registrar/` |
| CPA Mint | 通过 CPA 平台 mint Grok 认证 | `cpamint/` |
| 浏览器 MCP | 为非 Grok 模型提供 web_search/web_fetch 工具 | `browseruse/`, `agentbridge/` |
| 局域网访问 | 扫码配对后远程管理 | `remoteaccess/`, `server/` |
| SSH 远程 | SSH 连接管理与远程文件操作 | `ssh/` |
| 菜单栏/托盘 | macOS 菜单栏常驻 | `tray/`, `gui_tray_darwin*` |
| 缓存统计 | Token 缓存命中率统计 | `cachestats/`, `server/` |
| 会话图谱 | 跨供应商会话分支管理 | `server/session_graph.go` |

---

## 5. internal/ 子模块职责说明

### 5.1 server — HTTP 服务器与 API 处理器

| 文件 | 职责 |
|------|------|
| `server/server.go` | `Server` 结构体定义，HTTP 路由注册（`routes` 方法），生命周期管理（Listen/Shutdown），LAN 访问重配置 |
| `server/routing.go` | 路由策略的读取、应用与部分合并更新；`handleRouting` / `handleRoutingPolicy` / `handleRoutingReapply` |
| `server/subscription_proxy.go` | 订阅代理状态、登录、账号管理、模型列表的 HTTP 处理器；`SubscriptionProxy` 接口定义 |
| `server/subscription_inference.go` | 订阅推理转发：将 Grok CLI 的请求转发到 CLIProxyAPI（`http://127.0.0.1:8317`），修复畸形 tool history |
| `server/agent.go` | Grok Agent 的 HTTP 桥接：启动/停止/取消/会话管理/WebSocket 事件流；`AgentService` 接口定义 |
| `server/codebuddy.go` | CodeBuddy 推理端（`/codebuddy/v1/chat/completions` 和 `/codebuddy/v1/models`） |
| `server/codebuddy_profile.go` | 将本地 CodeBuddy CLI 的模型列表暴露为 GS 的 Profile |
| `server/grokpool.go` | Grok 号池 CRUD、批量导入、巡检、健康分类的 HTTP 处理器 |
| `server/grokauth.go` | Grok 官方认证状态的 HTTP 处理器 |
| `server/registrar.go` | 账号注册机的配置、启动、停止、探针、日志 HTTP 处理器 |
| `server/cpamint.go` | CPA Mint 会话管理 HTTP 处理器 |
| `server/session_graph.go` | 跨供应商会话图谱存储（`session_graph.json`），记录逻辑会话与分支 |
| `server/cache_stats.go` | 缓存统计 HTTP 处理器，调用 `cachestats.Collect` |
| `server/browseruse_policy.go` | 根据路由目标模型是否为 Grok 系列，决定是否注入 browser-use MCP 服务器 |
| `server/lan_access.go` | 局域网访问控制中间件：来源校验、Session Cookie、Pairing Code、QR 码生成 |

### 5.2 switcher — 配置切换引擎

| 文件 | 职责 |
|------|------|
| `switcher/switcher.go` | `Switcher` 结构体：将 Profile 或路由快照写入 `config.toml`；备份管理（创建/列出/还原/清理）；原子写入工具 `atomicWrite` |

### 5.3 profiles — 供应商档案存储

| 文件 | 职责 |
|------|------|
| `profiles/model.go` | `Profile` 与 `ModelDef` 数据结构；`Normalize` 方法（填充默认值、推断 APIBackend）；`Matches` 方法（用于检测当前配置是否匹配某 Profile） |
| `profiles/store.go` | `Store` 结构体：JSON 文件的 CRUD 操作；损坏自动恢复（`recovery.BackupCorrupt`） |

### 5.4 routing — 路由策略引擎

| 文件 | 职责 |
|------|------|
| `routing/model.go` | `Provider`、`ModelRoute`、`RoutingPolicy`、`Snapshot` 数据结构；`Project` 方法（将 Profile 列表投影为路由表）；`ProjectWithPolicy`（合并策略）；`Hydrate`（注入运行时凭证）；`RepairPolicy`（策略修复） |
| `routing/store.go` | `Store` 结构体：`routing.json` 的持久化；`Initialize` 方法（从 legacy Profile 一次性迁移）；损坏自动恢复 |

### 5.5 config — TOML 配置读写

| 文件 | 职责 |
|------|------|
| `config/tomlio.go` | TOML 文档解析与写入；`ImportProfile`（从 config.toml 导入 Profile）；`ApplyProfileToFile`（应用 Profile 到文件）；`ApplyRoutingToFile`（应用路由快照）；`UseOfficialAuthToFile`（切换到官方认证）；`ApplyPrivacyProtectionToFile` |
| `config/routing.go` | `PreviewRouting`（预览路由生成的 TOML）、`CurrentMatchesRouting`（检查当前配置是否匹配路由） |

### 5.6 agentbridge — Grok Agent ACP 桥接

| 文件 | 职责 |
|------|------|
| `agentbridge/bridge.go` | `Bridge` 结构体：管理与 Grok CLI ACP 连接的生命周期；会话管理、权限响应、状态跟踪；`SessionLoadError` 定义 |
| `agentbridge/client.go` | ACP 客户端连接管理（`acp.ClientSideConnection`） |
| `agentbridge/events.go` | 事件类型定义与转换 |
| `agentbridge/history.go` | 历史会话元数据存储与读取 |
| `agentbridge/notification_filter.go` | 通知队列溢出过滤 |
| `agentbridge/resolve.go` | Grok CLI 路径解析 |
| `agentbridge/subscriptions.go` | 事件订阅管理 |
| `agentbridge/process_other.go` | 跨平台进程管理 |

### 5.7 cliproxy — CLIProxyAPI 集成

| 文件 | 职责 |
|------|------|
| `cliproxy/cliproxy.go` | `Manager` 结构体：CLIProxyAPI 生命周期管理（安装/启动/停止/状态）；内置二进制校验（SHA-256 + Mach-O arm64 验证） |
| `cliproxy/adapter.go` | `UpdateAccount` / `DeleteAccount`：直接文件操作 CLIProxyAPI 的账号存储 |
| `cliproxy/runtime_darwin.go` | macOS LaunchAgent 管理；`DarwinKeychain` 钥匙串存取 |
| `cliproxy/runtime_other.go` | 非 macOS 平台的运行时适配 |
| `cliproxy/oauth_bridge.go` | OAuth 回调桥接：监听 `127.0.0.1:1455` 并将浏览器回调转发至 CLIProxyAPI 管理 API |

### 5.8 grokpool — Grok 号池管理

| 文件 | 职责 |
|------|------|
| `grokpool/manager.go` | `Manager` 结构体：号池生命周期（加载/保存/导入/巡检/调度/监听）；健康分类与坏号隔离 |
| `grokpool/store.go` | 号池状态持久化（`pool.json`） |
| `grokpool/types.go` | 号池数据类型定义（`ImportFile`、`ImportResult`、`Settings`） |
| `grokpool/dirimport.go` | 目录扫描导入 |
| `grokpool/inspect.go` | 账号巡检逻辑 |

### 5.9 grokauth — Grok 官方认证存储

| 文件 | 职责 |
|------|------|
| `grokauth/store.go` | `Store` 结构体：OAuth 凭证（access_token / refresh_token）的存储与刷新；`Credential` 数据结构；上游 URL 常量 |

### 5.10 codebuddy — CodeBuddy CLI 运行器

| 文件 | 职责 |
|------|------|
| `codebuddy/runner.go` | `Runner` 结构体：前台非持久化调用 CodeBuddy CLI（`--print --output-format stream-json --permission-mode acceptEdits --tools default`）；`DefaultArgs` 安全基线 |
| `codebuddy/discovery.go` | CodeBuddy CLI 发现与状态检查（版本、可用模型列表、fallback 模型） |
| `codebuddy/events.go` | CodeBuddy 事件类型定义 |
| `codebuddy/prompt.go` | Prompt 构造逻辑 |

### 5.11 registrar — 账号自动注册机

| 文件 | 职责 |
|------|------|
| `registrar/service.go` | `Service` 结构体：注册机配置、任务管理、日志追踪 |
| `registrar/browser.go` | 浏览器自动化注册流程 |
| `registrar/cloudflare.go` | Cloudflare Turnstile 验证码处理 |
| `registrar/hotmail.go` | Hotmail 邮箱注册 |
| `registrar/mail.go` | 邮箱验证（IMAP） |
| `registrar/mint.go` | 注册后的账号 mint 流程 |
| `registrar/probe.go` | 账号可用性探针 |
| `registrar/store.go` | 注册机状态持久化 |
| `registrar/types.go` | 数据类型定义 |

### 5.12 cpamint — CPA Mint 认证

| 文件 | 职责 |
|------|------|
| `cpamint/mint.go` | `Service` 结构体：CPA Mint 会话管理（创建/轮询/完成/失败）；设备码 OAuth 流程 |
| `cpamint/schema.go` | CPA Mint 数据结构定义 |

### 5.13 browseruse — 浏览器 MCP 服务

| 文件 | 职责 |
|------|------|
| `browseruse/mcp.go` | `Server` 结构体：MCP JSON-RPC 2.0 over stdio 服务器；`web_search` / `web_fetch` 工具实现（基于 chromedp 无头 Chrome）；`IsGrokModel` 判断模型是否为 Grok 系列 |

### 5.14 netproxy — HTTP 代理传输层

| 文件 | 职责 |
|------|------|
| `netproxy/proxy.go` | `BuildTransport`：解析代理地址字符串，构建 `*http.Transport`；支持 http/https/socks5/socks5h 协议 |

### 5.15 notify — 系统通知

| 文件 | 职责 |
|------|------|
| `notify/notify.go` | `Info`：macOS `osascript` 桌面通知；`OpenPath`：跨平台打开文件/目录 |

### 5.16 autostart — 开机自启

| 文件 | 职责 |
|------|------|
| `autostart/autostart_darwin.go` | macOS LaunchAgent plist 生成与安装（`~/Library/LaunchAgents/com.grokbuildswitch.app.plist`） |
| `autostart/autostart_darwin.go` | macOS LaunchAgent 自启 |
| `autostart/autostart_other.go` | 其他平台空实现 |

### 5.17 crash — 崩溃恢复与日志

| 文件 | 职责 |
|------|------|
| `crash/crash.go` | `Setup`：打开日志文件，重定向 `os.Stderr` 和标准 `log` 包；`RecoverMainThread`：主线程 panic 恢复；`ReportFatal`：致命错误对话框 |
| `crash/dialog_other.go` | 错误对话框实现 |

### 5.18 recovery — 损坏文件恢复

| 文件 | 职责 |
|------|------|
| `recovery/recovery.go` | `BackupCorrupt`：将不可读的持久化文件重命名为时间戳备份（`.corrupt-{timestamp}.bak`），保留原扩展名 |

### 5.19 singleinstance — 单实例锁

| 文件 | 职责 |
|------|------|
| `singleinstance/singleinstance_darwin.go` | macOS：基于 `unix.Flock` 的排他文件锁 |
| `singleinstance/singleinstance_darwin.go` | macOS：基于 `unix.Flock` 的排他文件锁 |
| `singleinstance/singleinstance_other.go` | 其他平台空实现 |

### 5.20 remoteaccess — 局域网会话管理

| 文件 | 职责 |
|------|------|
| `remoteaccess/store.go` | `Store` 结构体：Session Token 与 Pairing Code 的生成/验证/过期管理 |

### 5.21 ssh — SSH 远程连接

| 文件 | 职责 |
|------|------|
| `ssh/handler.go` | `Handler` 结构体：SSH 连接配置的 CRUD HTTP 处理器注册 |
| `ssh/client.go` | SSH 连接管理与远程文件操作 |

### 5.22 settings — 应用设置存储

| 文件 | 职责 |
|------|------|
| `settings/settings.go` | `Store` 结构体：`settings.json` 的读写；`Settings` 数据结构（Port、Theme、Autostart、LANAccess、ProviderOrder 等）；端口范围校验（1024-65535）；损坏自动恢复 |

### 5.23 paths — 跨平台路径解析

| 文件 | 职责 |
|------|------|
| `paths/paths.go` | `Paths` 结构体：解析所有跨平台路径（DataDir、GrokHome、GrokConfig、ProfilesFile、RoutingFile 等）；`Ensure` 方法（创建目录 + 从 legacy `~/.grok_switch` 迁移）；`copyTree` 安全目录复制（拒绝符号链接） |

### 5.24 tray — 托盘控制器

| 文件 | 职责 |
|------|------|
| `tray/tray.go` | `Tray` 结构体：托盘应用控制器；`RunWithExternalLoop` 集成 systray；菜单构建、快速切换、设置/日志目录打开 |

### 5.25 cachestats — 缓存命中率统计

| 文件 | 职责 |
|------|------|
| `cachestats/stats.go` | `Stats` 结构体：从 Grok CLI 日志中聚合 token 缓存命中数据；按会话/全局统计 |

---

## 6. 数据持久化路径

### 6.1 macOS 路径

| 用途 | 路径 | 文件 | 敏感 |
|------|------|------|------|
| **DataDir** | `~/Library/Application Support/Grok Build Switch/` | — | 是 |
| Grok Home | `~/.grok/`（`$GROK_HOME`） | — | 是 |
| Grok Config | `~/.grok/config.toml`（`$GROK_CONFIG`） | 生效配置 | **是** |
| Profiles | DataDir | `profiles.json` | **是** |
| Routing | DataDir | `routing.json` | **是** |
| Settings | DataDir | `settings.json` | 否 |
| Remote Access | DataDir | `remote_access.json` | 是 |
| Grok Auth | DataDir | `grok_auth.json` | **是** |
| Grok Pool | DataDir | `grok_pool/pool.json` + `accounts/` | **是** |
| Backups | DataDir | `backups/config-*.toml` | **是** |
| Session Graph | DataDir | `session_graph.json` | 否 |
| CLIProxyAPI | DataDir | `cliproxy/bin/CLIProxyAPI`、`config.yaml`、`auth/`、`logs/` | **是** |
| Registrar | DataDir | `registrar/registrar.json`、`jobs/`、`accounts_cli.txt` | **是** |
| Agent Log | DataDir | `agent.log` | 否 |
| App Log | DataDir | `grok_switch.log` | 否 |
| Single Instance | `/tmp/` | `grok_switch-{hash}.lock` | 否 |
| Launch Agent | `~/Library/LaunchAgents/` | `com.grokbuildswitch.app.plist` | 否 |
| Legacy DataDir | `~/.grok_switch/` | — | 是 |

> **迁移逻辑**：首次运行时，`paths.Resolve()` 检测到 `~/Library/Application Support/Grok Build Switch/` 不存在时，会从 `~/.grok_switch/` 迁移旧数据，并写入 `.migrated-from-dot-grok-switch` 标记文件。

### 6.2 路径环境变量覆盖

| 变量 | 作用 |
|------|------|
| `GROK_HOME` | 覆盖 Grok Home 目录（默认 `~/.grok`） |
| `GROK_CONFIG` | 覆盖 Grok Config 路径（默认 `~/.grok/config.toml`） |
| `GROK_SWITCH_HOME` | 覆盖 GS 的 DataDir |

---

## 7. HTTP API 路由总览

服务器默认监听 `127.0.0.1:17878`（开启 LAN 访问后为 `0.0.0.0:17878`）。

### 7.1 认证与配对

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/pair` | POST | `server.go` | 局域网配对码验证 |

### 7.2 状态与设置

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/api/status` | GET | `server.go` | 全局状态（active profile、routing、settings、port） |
| `/api/settings` | GET/PUT | `server.go` | 应用设置读写 |
| `/api/lan-access` | GET/PUT | `server.go` | 局域网访问开关 |

### 7.3 Profile 与路由

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/api/profiles` | GET/POST | `server.go` | Profile 列表 / 创建 |
| `/api/profiles/{id}` | PUT/DELETE | `server.go` | Profile 更新 / 删除 |
| `/api/official/activate` | POST | `server.go` | 切换到官方账号 |
| `/api/routing` | GET | `routing.go` | 读取路由快照 |
| `/api/routing/policy` | PUT | `routing.go` | 更新路由策略（支持部分合并） |
| `/api/routing/reapply` | POST | `routing.go` | 重新应用当前路由策略 |

### 7.4 配置导入与备份

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/api/import` | POST | `server.go` | 从当前 config.toml 导入 Profile |
| `/api/backups` | GET | `server.go` | 列出配置备份 |
| `/api/backups/{file}/restore` | POST | `server.go` | 还原指定备份 |
| `/api/config` | GET/PUT | `server.go` | 直接读写 config.toml |
| `/api/config/preview` | POST | `server.go` | 预览路由生成的 TOML |
| `/api/config/privacy` | POST | `server.go` | 应用隐私保护配置 |

### 7.5 模型与连接

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/api/models/fetch` | POST | `server.go` | 从上游拉取可用模型列表 |
| `/api/models/reasoning-efforts` | POST | `server.go` | 探测模型支持的推理强度 |
| `/api/connection/test` | POST | `server.go` | 测试连接配置 |

### 7.6 Grok Auth 与号池

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/api/grok-auth` | GET/POST | `server.go` | Grok 官方认证状态 / 导入 |
| `/api/grok-auth/refresh` | POST | `server.go` | 刷新 Grok 官方 token |
| `/api/grok-pool` | GET/POST/PUT | `grokpool.go` | 号池状态 / 导入 / 设置 |
| `/api/grok-pool/inspect` | POST | `grokpool.go` | 触发号池巡检 |
| `/api/grok-pool/bulk` | POST | `grokpool.go` | 批量操作 |
| `/api/grok-pool/import-dir` | POST | `cpamint.go` | 从目录导入号池 |
| `/api/grok-pool/open-auth-dir` | POST | `grokpool.go` | 打开认证目录 |
| `/api/grok-pool/accounts/{id}` | PUT/DELETE | `grokpool.go` | 账号更新 / 删除 |

### 7.7 CPA Mint 与注册机

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/api/cpa-mint` | POST/GET | `cpamint.go` | CPA Mint 会话管理 |
| `/api/registrar` | GET/PUT | `registrar.go` | 注册机配置 |
| `/api/registrar/probe` | POST | `registrar.go` | 注册机探针 |
| `/api/registrar/start` | POST | `registrar.go` | 启动注册任务 |
| `/api/registrar/stop` | POST | `registrar.go` | 停止注册任务 |
| `/api/registrar/job` | GET | `registrar.go` | 查询注册任务状态 |
| `/api/registrar/job/log` | GET | `registrar.go` | 查询注册任务日志 |

### 7.8 Agent 对话

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/api/agent/status` | GET | `agent.go` | Agent 运行状态 |
| `/api/agent/start` | POST | `agent.go` | 启动 Agent |
| `/api/agent/stop` | POST | `agent.go` | 停止 Agent |
| `/api/agent/cancel` | POST | `agent.go` | 取消当前 Prompt |
| `/api/agent/session` | POST | `agent.go` | 新建会话 |
| `/api/agent/session/load` | POST | `agent.go` | 加载历史会话 |
| `/api/agent/sessions` | GET | `agent.go` | 列出历史会话 |
| `/api/agent/sessions/{id}` | GET | `agent.go` | 获取会话历史 |
| `/api/agent/session/rename` | POST | `agent.go` | 重命名会话 |
| `/api/agent/ws` | GET | `agent.go` | WebSocket 事件流 |

### 7.9 CodeBuddy

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/api/codebuddy/status` | GET | `codebuddy.go` | CodeBuddy CLI 状态 |
| `/codebuddy/v1/models` | GET | `codebuddy.go` | 模型列表 |
| `/codebuddy/v1/chat/completions` | POST | `codebuddy.go` | 聊天推理（仅本机） |

### 7.10 订阅代理

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/api/subscription-proxy` | GET | `subscription_proxy.go` | 订阅代理状态 |
| `/api/subscription-proxy/service` | POST | `subscription_proxy.go` | 服务操作（start/stop） |
| `/api/subscription-proxy/login` | POST | `subscription_proxy.go` | 发起登录 |
| `/api/subscription-proxy/login/open` | POST | `subscription_proxy.go` | 打开登录 URL |
| `/api/subscription-proxy/accounts/{id}` | PUT/DELETE | `subscription_proxy.go` | 账号更新 / 删除 |
| `/api/subscription-proxy/models` | GET | `subscription_proxy.go` | 可用模型列表 |
| `/api/subscription-proxy/providers` | GET | `subscription_proxy.go` | 可用供应商列表 |
| `/api/subscription-proxy/diagnostics` | GET | `subscription_proxy.go` | 诊断检查 |
| `/subscription-proxy/v1/chat/completions` | POST | `subscription_inference.go` | 订阅推理转发（仅本机） |

### 7.11 Grok 代理

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/grok/v1/chat/completions` | POST | `subscription_inference.go` | Grok 官方代理转发 |

### 7.12 其他

| 路由 | 方法 | 处理器文件 | 说明 |
|------|------|-----------|------|
| `/api/cache-stats` | GET | `cache_stats.go` | 缓存命中率统计 |
| `/api/ssh/connections` | GET/POST | `ssh/handler.go` | SSH 连接列表 / 创建 |
| `/api/ssh/connections/{id}` | PUT/DELETE | `ssh/handler.go` | SSH 连接更新 / 删除 |
| `/api/ssh/connect` | POST | `ssh/handler.go` | 建立 SSH 连接 |
| `/api/ssh/disconnect/{id}` | POST | `ssh/handler.go` | 断开 SSH 连接 |
| `/api/ssh/files` | GET | `ssh/handler.go` | 远程文件列表 |
| `/api/ssh/preview` | GET | `ssh/handler.go` | 远程文件预览 |
| `/api/ssh/save` | POST | `ssh/handler.go` | 远程文件保存 |
| `/api/ssh/import-ssh-config` | POST | `ssh/handler.go` | 导入 SSH 配置 |
| `/` | GET | `server.go` | 静态文件服务（Web UI） |

---

## 8. 构建与运行方式

### 8.1 从源码构建（macOS）

**前置条件**：
- Go 1.26.5+
- macOS Apple Silicon (arm64)
- Xcode Command Line Tools（cgo 必需）

**构建命令**：
```bash
cd grok-build-switch

# 运行测试
go test -mod=vendor ./...

# 构建应用
./build-macos.sh

# 构建产物：
#   dist/macos/Grok Build Switch.app
#   dist/macos/Grok-Build-Switch-{version}-macOS-arm64.dmg
```

**Build tags**：
- `wailsgui`：启用 Wails GUI 模式（菜单栏应用）
- `desktop`：桌面平台
- `production`：生产模式

**签名与公证**：
```bash
export APPLE_SIGNING_IDENTITY="Developer ID Application: ..."
export APPLE_NOTARY_PROFILE="notarytool-profile"
./build-macos.sh --require-signature
```

### 8.2 开发模式运行

```bash
# 无托盘，仅 HTTP 服务器
go run -mod=vendor . --no-tray

# 静默启动（不自动打开浏览器）
go run -mod=vendor . --silent
```

---

## 9. 常见问题定位指引

### 9.1 配置相关

| 症状 | 排查路径 | 关键文件 |
|------|---------|----------|
| 切换 Profile 后 config.toml 未更新 | 检查 `switcher.Switcher.Activate` 是否成功；查看 backups 目录是否有备份 | `switcher/switcher.go`, `config/tomlio.go` |
| 路由策略未生效 | 检查 `routing.json` 状态；`routingMu` 锁是否被持有 | `routing/store.go`, `server/routing.go` |
| Profile 列表为空 | 检查 `profiles.json` 是否存在且可读 | `profiles/store.go`, `paths/paths.go` |
| 配置损坏自动恢复 | 查找 `.corrupt-*.bak` 文件 | `recovery/recovery.go` |

### 9.2 订阅代理相关

| 症状 | 排查路径 | 关键文件 |
|------|---------|----------|
| CLIProxyAPI 未启动 | 检查 `cliproxy.Manager.Status()`；查看 `cliproxy/logs/` | `cliproxy/cliproxy.go` |
| OAuth 登录失败 | 检查 `127.0.0.1:1455` 端口是否被占用 | `cliproxy/oauth_bridge.go` |
| 订阅账号 token 过期 | 检查 `grok_pool` 账号状态与刷新逻辑 | `grokpool/manager.go` |
| 订阅代理推理失败 | 检查 CLIProxyAPI 日志；确认 `stream_tool_calls` 配置 | `server/subscription_inference.go` |

### 9.3 Agent 对话相关

| 症状 | 排查路径 | 关键文件 |
|------|---------|----------|
| Agent 无法启动 | 检查 `agent.log`；确认 Grok CLI 路径 | `agentbridge/resolve.go`, `agentbridge/bridge.go` |
| 会话加载失败 | 检查 `SessionLoadError`；查看通知队列溢出 | `agentbridge/notification_filter.go` |
| WebSocket 事件中断 | 检查 `handleAgentWebSocket` 状态 | `server/agent.go` |
| 工具调用失败 | 检查 ACP 连接状态；`stream_tool_calls` 配置 | `agentbridge/bridge.go` |

### 9.4 构建相关

| 症状 | 排查路径 | 关键文件 |
|------|---------|----------|
| `duplicate symbol _OBJC_METACLASS_$_AppDelegate` | 必须使用 `-mod=vendor`（不能用 `-mod=readonly`） | `build-macos.sh`, `vendor/fyne.io/systray/systray_darwin.m` |
| CLIProxyAPI 校验失败 | 检查 SHA-256 与 Mach-O arm64 架构 | `cliproxy/cliproxy.go` |
| 签名失败 | 检查 extended attributes：`xattr -cr` | `build-macos.sh` |

### 9.5 运行时排查

| 排查项 | 方法 |
|--------|------|
| 应用日志 | 打开 DataDir 下的 `grok_switch.log`（托盘菜单 → 打开日志目录） |
| Agent 日志 | DataDir 下的 `agent.log` |
| CLIProxyAPI 日志 | DataDir 下的 `cliproxy/logs/stdout.log` 和 `stderr.log` |
| HTTP 请求 | 浏览器 DevTools Network 面板；或 `curl http://127.0.0.1:17878/api/status` |
| 单实例锁 | `/tmp/grok_switch-*.lock` 是否存在 |
| 端口占用 | `lsof -i :17878` |

---

## 10. 关键依赖

| 依赖 | 用途 | 版本 |
|------|------|------|
| Go | 编程语言 | 1.26.5+ |
| Wails | GUI 框架（macOS 菜单栏） | v2.13.0 |
| fyne.io/systray | macOS NSStatusItem 菜单栏 | vendored |
| CLIProxyAPI | 本地订阅账号代理 | v7.2.94 |
| coder/acp-go-sdk | ACP 协议客户端 SDK | vendored |
| coder/websocket | WebSocket 通信 | vendored |
| pelletier/go-toml | TOML 解析与生成 | vendored |
| chromedp | 无头 Chrome 浏览器自动化（browser-use） | vendored |
| skip2/go-qrcode | QR 码生成（局域网配对） | vendored |
| golang.org/x/sys/unix | macOS 文件锁（Flock） | vendored |
| grok-build-switch 自身 | 核心业务逻辑 | — |

---

## 11. 模型切换机制详解

> 本章解释 GS 中"模型切换"的两种机制、它们与 Grok CLI 的交互关系，以及会话图谱中分支的产生逻辑。

### 11.1 三种模型切换路径

GS 生态中存在三种改变"当前使用模型"的方式，它们的触发条件、上下文保留、风险各不相同：

| 方式 | 触发者 | 上下文保留 | 风险 |
|------|--------|-----------|------|
| CLI `/model` | 用户在 CLI 手动输入 | 完整保留 | 高（tool schema 残留、上下文溢出） |
| Switch 改策略（同供应商） | 用户在 Switch 改 default | 不影响当前会话 | 无（只影响下次新会话） |
| Switch handoff（跨供应商） | 用户在 Switch 改 default 且供应商变化 | 有损迁移（纯文本摘要） | 中（摘要可能丢失细节） |

### 11.2 机制一：CLI `/model`（同一会话内切换）

```
用户在 CLI 输入 /model gpt-5.4
    ↓
Grok CLI 切换后端模型（session 不变）
    ↓
后续消息发给新模型
```

**特点**：
- 同一 session ID 不变
- 对话历史（含 tool_calls、reasoning）原封不动喂给新模型
- 无清理、无适配

**风险场景**：
- 上下文窗口溢出（LongCat 1M → GPT 200k，对话超长时触发 `SessionLoadError`）
- Tool schema 残留（旧模型的工具调用格式与新模型不兼容）
- Reasoning 痕迹不兼容（LongCat 的 thinking blocks 被 GPT 当乱码）

**适用场景**：仅建议在**短对话**、**同家族模型**间切换。

### 11.3 机制二：Switch 改策略（不影响当前会话）

```
用户在 Switch 把 default 从 LongCat-2.0 改成 k3（同属一个供应商）
    ↓
Switch 写入新 routing.json + config.toml
    ↓
当前会话：完全不受影响，继续用 LongCat-2.0
下次 /new：用新 default（k3）启动
```

**特点**：
- 运行中的 Agent 不感知变化
- 无 migration、无 handoff
- 只有下次新会话启动时生效

### 11.4 机制三：Switch handoff（跨供应商自动迁移）

```
用户在 Switch 把 default 从 LongCat-2.0 改成 gpt-5.4（不同供应商）
    ↓
Switch 检测到 active_provider ≠ target_provider
    ↓
如果 Agent 空闲 → 执行 prepareAgentForProviderSwitch
    ↓
创建 providerHandoff（Mode: text_migration）
    ↓
调用 commitProviderHandoff：
  1. 读取旧会话历史
  2. 生成纯文本迁移摘要（排除 tool_calls、reasoning、IDs）
  3. 通过 ACP 创建新会话，注入摘要作为第一条用户消息
  4. 旧会话变只读
  5. Agent 自动切换到新会话
    ↓
用户发下一条消息 → 用新模型（GPT-5.4）回复
```

**特点**：
- **用户无需任何 CLI 操作**（不需要 `/model`、`/new`、`resume`）
- 自动创建新会话、自动切换
- 旧会话保留在会话图谱中（只读）
- 会话图谱产生新分支

**前提条件**：
- Agent 必须正在运行（没关闭 CLI）
- Agent 必须空闲（不在回复中，否则 Switch 拒绝切换）

### 11.5 text_migration 的内容与限制

迁移摘要由 `StoredSessionTransferText`（`agentbridge/history.go:333`）生成：

**包含的内容**：
- 纯文本 `user` 消息
- 纯文本 `assistant` 消息
- 旧会话标题
- 提示语："请继续完成原任务；不要假设旧工具调用仍然存在"

**排除的内容**：
- reasoning / thinking blocks
- tool_calls（函数调用）
- tool_results（工具输出）
- 协议元数据（session_id、message_id 等）
- 任何 secret 内容

**字符上限**：48000 字符（约 16000-24000 中文字），超出截断。

**示例**：

```
以下是从另一供应商安全迁移的旧会话纯文本上下文。
请继续完成原任务；不要假设旧工具调用仍然存在，需要时重新检查文件和环境。
旧会话标题：实现 Web Search 能力标记与回退

用户：读取 config.toml 并把 default 改成 k3
助手：已改成 k3

用户：再检查一下 web_search 路由是否正确
助手：web_search 已指向 codebuddy/hy3
```

**注意**：新模型看到"已改成 k3"但不知道 config.toml 当前内容。如果让它"再检查一下"，它看不到之前 tool_result 里的文件内容——它需要重新读取。

### 11.6 会话图谱中的分支

每次 Switch handoff 会在会话图谱中产生一条新分支：

```
逻辑会话：grok-switch
  ├── 分支 1：LongCat-2.0（旧，只读）
  └── 分支 2：GPT-5.4（新，可写）← 当前活跃
```

- 每个分支关联独立的 `provider_id`、`model`、`base_url`
- Resume 旧分支 → 用旧供应商配置恢复
- 当前分支 → 用新供应商配置交互

### 11.7 常见用户场景

#### 场景 A：我想从零开始用新模型

```
操作：在 CLI 里输入 /new
结果：全新空白会话，用当前 default 模型
```

#### 场景 B：我想带着上下文换模型

```
操作：直接在 Switch 里改 default（Agent 保持空闲）
结果：Switch 自动执行 handoff，当前界面下一条消息用新模型
```

#### 场景 C：我正在回复中，想切模型

```
操作：等待当前回复完成
结果：Agent 变空闲后，Switch 才允许切换
```

#### 场景 D：我先关了 CLI，后来改了 default

```
操作：关闭 CLI → 改 default → 重新打开 CLI → resume 旧会话
结果：用旧模型恢复（因为旧会话保存的是旧供应商配置）
     新 default 只影响下次 /new
```

### 11.8 关键结论

1. **Switch handoff 是自动的**——不需要 `/model`、`/new`、`resume`
2. **Agent 必须空闲**——回复中拒绝切换
3. **text_migration 是有损的**——只保留纯文本，丢掉工具调用细节
4. **长对话切换有风险**——上下文窗口不匹配可能溢出
5. **会话图谱是安全网**——旧分支永远可恢复

---

> **文档维护**：本文档随代码演进持续更新。如发现描述与代码不符，以代码为准并请更新本文档。
