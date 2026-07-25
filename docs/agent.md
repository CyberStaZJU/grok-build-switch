# Grok Build Switch — Agent 文档

> 面向维护者的架构与约定手册。写给你自己，也写给下一个接手的人。

---

## 1. 项目定位

Grok Build Switch 是一个 macOS 桌面应用，作为 **Grok CLI 的统一代理与供应商路由器**。它让用户在多个 AI 供应商（OpenAI 兼容、Anthropic、CodeBuddy、订阅代理、官方 Grok 等）之间切换，而无需手动编辑 `~/.grok/config.toml`。

核心能力：
- **多供应商路由**：一个 GUI 管理多个 Profile，每个 Profile 含多个模型定义。
- **会话保持**：跨供应商切换时，通过逻辑会话图（session graph）保留对话上下文。
- **订阅代理**：内嵌 CLIProxyAPI，把 Grok 的请求转发到 ChatGPT/Gemini 订阅账号。
- **CodeBuddy 集成**：本地代码助手，纯文本、只读、无工具调用。
- **ACP Agent 管理**：通过 `grok agent stdio` 启动/管理 Grok Agent 子进程。

---

## 2. 入口点与构建标签

| 文件 | 构建标签 | 作用 |
|:---|:---|:---|
| `main.go` | `!wailsgui` | 默认入口：HTTP 服务器 + 系统托盘 + 浏览器 UI |
| `gui_main.go` | `wailsgui` | Wails 桌面 GUI 入口 |

构建命令（`build-macos.sh`）：
```bash
go build -tags wailsgui,desktop,production -o "Grok Build Switch.app" .
```

关键构建标签：
- `wailsgui`：启用 Wails 桌面 GUI（默认关闭，走托盘+浏览器）。
- `desktop`：启用桌面平台特性。
- `production`：启用生产模式（崩溃日志、单实例锁等）。

---

## 3. 模块布局（`internal/`）

```
internal/
├── agentbridge/     # ACP Agent 子进程管理（启动/停止/会话/工具/权限）
├── autostart/       # 开机自启动（LaunchAgent / 注册表）
├── cachestats/      # Grok 缓存统计
├── cliproxy/        # CLIProxyAPI 生命周期管理（内嵌二进制、启动/停止）
├── codebuddy/       # CodeBuddy CLI 发现与调用
├── config/          # TOML 读写（tomlio.go）、路由到 Profile 的映射（routing.go）
├── cpamint/         # CPA Mint 服务
├── crash/           # 崩溃捕获与报告
├── grokauth/        # 官方 Grok 认证状态
├── grokpool/        # Grok 认证目录池管理
├── netproxy/        # 网络代理
├── notify/          # 系统通知
├── paths/           # 数据目录路径解析与迁移
├── profiles/        # Profile 存储（profiles.json）
├── recovery/        # 损坏文件备份恢复
├── registrar/       # 账号注册服务
├── remoteaccess/    # 局域网访问会话
├── routing/         # 路由模型与策略（routing.json）
├── server/          # HTTP 服务器与所有 API 处理器
├── settings/        # 应用设置（settings.json）
├── singleinstance/  # 单实例锁
├── ssh/             # SSH 远程访问
├── switcher/        # config.toml 事务性切换与备份
└── tray/            # 系统托盘 UI
```

---

## 4. 数据流：一次供应商切换

```
用户点击「启用」
  → handleProfileByID (POST /api/profiles/:id/activate)
    → prepareAgentForProviderSwitch(target)
      → 检查是否同供应商（sameProvider）
      → 若不同：读取旧会话历史 → 生成迁移文本 → 记录逻辑会话检查点
    → activateProfileRouting(id) 或 sw.Activate(id)
      → routing.ProjectWithPolicy → 重建路由目录
      → grokconfig.PreviewRouting → 验证 TOML 可写
      → sw.ApplyRouting → 备份 + 原子写入 config.toml
      → routing.Replace → 持久化路由策略
      → profiles.SetActive → 更新 active 标记
    → commitProviderHandoff(handoff)
      → 停止旧 Agent 子进程
      → 保存 handoff 状态（供下次 Agent.Start 消费）
    → Agent.Start → 新会话 → 注入迁移文本
```

**事务回滚**：任何步骤失败时，按逆序回滚：恢复 config.toml、恢复 routing.json、恢复 active profile。

---

## 5. 核心数据结构

### 5.1 Profile（`internal/profiles/model.go`）

```go
type Profile struct {
    ID, Name, Source, Template string
    UpstreamFormat string            // openai_chat / openai_responses / anthropic / ...
    BaseURL, APIKey string
    AvailableModels []string
    DefaultModel, DefaultReasoningEffort string
    WebSearchModel string               // 联网搜索模型（可空）
    SubagentsModels struct{ Explore, Plan string }
    Models []ModelDef                   // 完整模型定义列表
    IsActive bool
    WebSearchCapable bool               // 运行时计算，不序列化
}

type ModelDef struct {
    Name, Model, BaseURL, APIKey, APIBackend string
    SupportsBackendSearch, SupportsReasoningEffort bool
    ReasoningEfforts []string
    ContextWindow, MaxCompletionTokens int64
}
```

### 5.2 Routing（`internal/routing/model.go`）

```go
type RoutingPolicy struct {
    Official bool
    Default, DefaultReasoningEffort string
    WebSearch string
    WebSearchCapable bool
    Subagents struct{ Explore, Plan string }
}

type ModelRoute struct {
    ID, Name, ProviderID, ProfileModel string
    Model, APIBackend, BaseURL, APIKey string  // 运行时注入，不序列化
    SupportsBackendSearch, SupportsReasoningEffort bool
    ReasoningEfforts []string
}

type Snapshot struct {
    Version int
    Providers []Provider
    ModelRoutes []ModelRoute
    Policy RoutingPolicy
    UpdatedAt time.Time
    Hydrated bool  // 标记是否已注入运行时凭证
}
```

### 5.3 Session Graph（`internal/server/session_graph.go`）

```
logicalSession (稳定 ID)
  ├── branch: {providerID|backend|baseURL} → native session (provider A)
  └── branch: {providerID|backend|baseURL} → native session (provider B)
```

- 逻辑会话 ID 在跨供应商切换时保持不变。
- 每个供应商分支保留自己的原生 session ID。
- 健康标记：`healthy` / `degraded`。

---

## 6. 持久化文件

| 文件 | 位置 | 作用 |
|:---|:---|:---|
| `profiles.json` | `~/Library/Application Support/Grok Build Switch/` | 所有供应商 Profile |
| `routing.json` | 同上 | 路由目录与策略（凭证已脱敏） |
| `settings.json` | 同上 | 应用设置（端口、自启动等） |
| `session_graph.json` | 同上 | 逻辑会话图 |
| `grok_switch.log` | 同上 | 运行日志 |
| `config.toml` | `~/.grok/config.toml` | Grok CLI 的实时配置（Switch 写入） |
| `backups/` | 数据目录 | config.toml 自动备份（最多 10 份） |

---

## 7. 约定与禁忌

### 7.1 安全
- **永不序列化凭证到 routing.json**：`sanitizedSnapshot` 在写入前清除所有 BaseURL/APIKey/ExtraHeaders。
- **永不日志输出 API Key**：`publicCodeBuddyStatus` 等函数会脱敏。
- **config.toml 写入必须原子**：`atomicWrite` 使用 tempfile + rename。

### 7.2 单例
- **只运行一个 CLIProxyAPI 进程**：`cliproxy.Manager` 保证。
- **只运行一个 grok_switch 实例**：`singleinstance.Acquire` 文件锁。

### 7.3 事务
- config.toml 修改前**必须备份**：`sw.Backup()`。
- 路由策略修改是**全量替换**：先 `PreviewRouting` 验证，再 `ApplyRouting` + `Replace`。
- 失败时必须**按逆序回滚**。

### 7.4 CodeBuddy
- **纯文本、只读**：不转发 tools/functions/parallel_tool_calls。
- **无 Edit/Write/Bash**：CodeBuddy 模型不能作为工具执行器。
- **无 `-y`、无 daemon**：每次调用都是独立进程。

### 7.5 web_search / x_search
- 只有 `api_backend == "responses" && supports_backend_search == true` 的模型才支持原生 `x_search`。
- 不支持的模型：`WebSearchCapable = false`，当前仅提示使用 browser-use（TODO：自动切换）。

---

## 8. 调试经验（踩坑记录）

### 8.1 stream_tool_calls 与空工具名

**现象**：`todo_write` 等工具调用报 "Tool not found"，工具名为空。Grok 日志出现 `shell.turn.action_stationarity_nudge` 且 `tool_name:""`。

**根因**：OpenAI 兼容 API 的 streaming tool_call 格式为增量 chunk——首个 chunk 含 `function.name`，后续 chunk 含 `function.arguments`。部分端点（如 `gpt-5.6-sol@API池`）返回的 chunk 中 `function.name` 缺失或为空，导致 Grok 解析失败，工具名丢失。

**诊断方法**：
1. 查看 Grok 日志中 `action_stationarity_nudge` 事件
2. 用 `curl` 直接请求上游 API，观察 SSE 流中 `tool_calls[0].function.name` 是否在第一帧就出现

**修复**：在 `~/.grok/config.toml` 的 `[models]` 下设置 `stream_tool_calls = false`，强制工具调用以完整 JSON 返回而非增量流式。对已知可靠的模型（LongCat-2.0、codebuddy/*）可单独设为 `true`。

**影响**：`stream_tool_calls = false` 不损失模型能力，仅改变传输格式——工具调用在完成后一次性返回，而非逐 token 拼接。

### 8.2 systray 与 Wails 的 AppDelegate 符号冲突

**现象**：macOS 构建报 `duplicate symbol _OBJC_METACLASS_$_AppDelegate`。

**根因**：Wails 框架和 `fyne.io/systray` 都在 vendored Objective-C 代码中定义了 `AppDelegate` 类，链接时产生重复符号。

**修复**：在 `vendor/fyne.io/systray/systray_darwin.m` 中将 `AppDelegate` 重命名为 `SystrayAppDelegate`（包括类名、实例变量类型、`alloc/init` 调用）。

**教训**：在 vendored Objective-C 代码中，类名应具备项目前缀，避免与宿主框架冲突。

### 8.3 路由策略的部分合并语义

**需求**：菜单栏常驻窗口需要在不覆盖其他字段的前提下，仅更新路由策略中的 `default` 字段。

**方案**：`handleRoutingPolicy` 先解码为 `map[string]json.RawMessage`，检测哪些字段存在，仅合并这些字段到已存储的策略中。`decodeRoutingJSON` 在目标类型为 `*map[string]json.RawMessage` 时允许未知字段。

**测试注意**：合并语义下，省略的字段保持旧值。测试必须发送显式的零值字段（如 `{"official":false,"default":"..."}`）来验证覆盖行为。

### 8.4 CLIProxyAPI PATCH 返回 404

**现象**：通过 CLIProxyAPI 管理 API 的 `PATCH /auth-files` 端点禁用或修改账号返回 404。

**根因**：CLIProxyAPI v7.2.94 的管理 API 仅支持 `GET /auth-files` 和 `DELETE /auth-files?name=...`，不支持 PATCH。

**修复**：`UpdateAccount` 和 `DeleteAccount` 改为直接操作 auth 文件（位于 `~/Library/Application Support/Grok Build Switch/cliproxy/auth/`），使用 `updateAuthFileDisabled` 和 `os.Remove`。

### 8.5 CodeBuddy Harness 的 stream-json 格式

**背景**：CodeBuddy CLI（`/opt/homebrew/bin/codebuddy`）是 Node.js agent 代理，`--output-format stream-json` 输出 NDJSON 流。

**格式解析**：
- 文本：`{"type":"content_block_start","content_block":{"type":"text"}}` + `{"type":"content_block_delta","delta":{"type":"text_delta","text":"..."}}`
- 工具调用：`content_block_start`（type=tool_use）→ 多个 `input_json_delta`（增量 JSON 片段）→ `content_block_stop`
- 工具结果：`{"type":"tool_result","tool_use_id":"...","content":"..."}`

**实现要点**：
- 需 `toolAccumulator` 结构收集跨多个 delta 的 `input_json` 片段
- `ParseEvent` 接收 `*toolAccumulator` 参数维护状态
- `content_block_stop` 时发射完整的 `EventToolUse`

**权限模式**：`--permission-mode acceptEdits` 允许工具修改文件；`--tools default` 启用全部工具。

### 8.6 菜单栏常驻（NSStatusItem）实现

**目标**：点红色叉后窗口隐藏到菜单栏，类似 TailScale/输入法。

**方案**：
- 使用 `fyne.io/systray` 的 `Register()` 进入外部循环模式（`RunWithExternalLoop`）
- Wails 的 `beforeClose` 回调返回 `true` 拦截关闭 → 调用 `WindowHide`
- 菜单栏点击「显示主窗口」→ `WindowShow`；点击「退出」→ 调用 systray 的 stopper 释放 Cocoa 主循环后 `Wails.Exit`

**构建标签分离**：`gui_tray_darwin_provider.go` 使用 `//go:build wailsgui && darwin`，避免与 Windows 的 `gui_tray_provider.go` 类型重复定义冲突。

**Wails 窗口方法**：通过 `wailsRuntime.Window.Show(runtimeContext)` 和 `wailsRuntime.Window.Hide(runtimeContext)` 控制。运行时上下文存储在 `guiTrayController.wailsRuntime`。

### 8.7 subagents 路由独立于 default

**现象**：用户设置 `default = gpt-5.6-sol` 后，子代理（explore/plan）仍使用 LongCat-2.0。

**根因**：`~/.grok/config.toml` 中 `[subagents.models]` 独立配置 `explore` 和 `plan` 模型，与 `[models].default` 是不同字段。

**修复方向**：在路由策略中统一设置 `[subagents.models]` 的 explore/plan，或在路由视图中增加对这些字段的可视化配置。

---

## 9. 常见操作

### 9.1 构建与测试
```bash
# 运行所有测试
go test ./...

# 带竞态检测
go test -race ./...

# 构建 macOS 应用（必须在 macOS Apple Silicon 上）
bash build-macos.sh

# 仅构建不打包
BUILD_DIR=./dist go build -tags wailsgui,desktop,production -o dist/grok_switch .
```

### 9.2 调试
- 日志：`~/Library/Application Support/Grok Build Switch/grok_switch.log`
- Agent 日志：`~/Library/Application Support/Grok Build Switch/agent.log`
- 崩溃日志：`crash.ShowInfo` 原生对话框

### 9.3 数据迁移
- 旧数据目录 `~/.grok_switch` → `~/Library/Application Support/Grok Build Switch/`
- 由 `paths.Resolve()` 自动处理。

### 9.4 备份与恢复
- config.toml 每次修改前自动备份到 `backups/`。
- 损坏的 JSON 文件由 `recovery.BackupCorrupt` 自动备份并重建。

---

## 10. 关键依赖

| 依赖 | 版本 | 用途 |
|:---|:---|:---|
| `go-toml` | v2.2.4 | TOML 解析/序列化 |
| `acp-go-sdk` | v0.13.5 | Agent Client Protocol |
| `chromedp` | v0.16.0 | 浏览器自动化（browser-use） |
| `wails` | v2.13.0 | 桌面 GUI 框架 |
| CLIProxyAPI | v7.2.94 | 订阅代理（内嵌二进制） |
| CodeBuddy | 外部 CLI | 本地代码助手 |

---

## 11. 术语表

| 术语 | 含义 |
|:---|:---|
| **Profile** | 一个供应商配置，含 URL、Key、模型列表 |
| **ModelRoute** | 路由目录中的一个模型条目（名称已消歧） |
| **RoutingPolicy** | 当前激活的路由选择（default/web_search/explore/plan） |
| **Hydrated Snapshot** | 已注入运行时凭证的内存路由快照（不序列化） |
| **Session Graph** | 逻辑会话到多供应商原生会话的映射 |
| **Provider Handoff** | 跨供应商切换时的会话迁移事务 |
| **WebSearchCapable** | 模型是否支持原生 x_search 工具 |
