# Grok Build Switch — Status 文档

> 当前状态、已知问题与技术债。最后更新：2026-07-27。

---

## 1. 当前状态

### 1.1 已稳定运行
- **多供应商路由**：Profile 创建/编辑/激活/删除，事务性 config.toml 切换。
- **会话图**：逻辑会话 ID + 多供应商分支，跨供应商切换时文本迁移。
- **订阅代理**：CLIProxyAPI 内嵌，ChatGPT/Gemini/Grok 订阅账号接入；状态/停止/重启/账号管理已修复。
- **CodeBuddy**：保持纯文本只读桥接（`Read/Grep/Glob`），忽略请求中可能因 `/model` 切换而残留的工具 schema；stream-json 文本与历史工具上下文可安全透传。
- **ACP Agent**：`grok agent stdio` 生命周期管理，session/load 溢出恢复。
- **构建系统**：`build-macos.sh` 签名 + DMG 打包 + 可选公证。
- **菜单栏常驻**：macOS NSStatusItem 实现，点红色叉隐藏到菜单栏，支持路由策略查看/切换、缓存统计、显示主窗口、退出。

### 1.2 已修复（近期）
- **GitHub 权威源与工作区生成物治理**：`CyberStaZJU/grok-build-switch` 已确定为代码/文档/发布记录的权威源，本机仅作工作副本。仓库根可重建 `dist/` 已在用户批准后永久删除；完整 `go test ./...` 通过。`ui/vendor/` 是 tracked `go:embed` 资产，必须保留；根 `vendor/` 是 `-mod=vendor` 离线依赖并含固定版本 CLIProxyAPI 包，暂不移动。当前 checkout 仍有未提交实现修改，因此在 commit/push 前不可视为可丢弃副本。
- **session/load 通知溢出**：`sessionLoadNotificationFilter` 抑制重放通知，`SessionLoadError` 自动重启恢复。
- **空工具名 400 错误**：`repairMalformedToolHistory` 在订阅代理请求层清洗 `input[].name` 为空的历史工具调用。
- **Subagents 配置键错误**：从 `subagents.default_model`（Grok 不识别）迁移到 `subagents.models.explore/plan`。
- **stream_tool_calls 导致工具名丢失**：部分 API 端点（如 gpt-5.6-sol@API池）返回的 streaming chunk 中 `function.name` 缺失，导致 Grok 解析出空工具名。修复：全局 `stream_tool_calls = false`，仅对已知可靠模型（LongCat-2.0、codebuddy/*）启用。
- **订阅代理状态/控制失效**：`SubscriptionProxyStatus` 扩展为包含 State/PID/ConfigPath/LastError/BaseURL/APIKeyMasked；`UpdateAccount`/`DeleteAccount` 改为直接操作 auth 文件（CLIProxyAPI PATCH 返回 404）。
- **菜单栏常驻**：通过 `fyne.io/systray` + `Register()` 外部循环模式实现 NSStatusItem；解决 systray 与 Wails 的 `AppDelegate` 符号冲突（重命名为 `SystrayAppDelegate`）。
- **CodeBuddy 工具上下文兼容**：stream-json 可解析历史 `tool_use/tool_result` 并作为引用文本返回；安全审查后保留 `Read/Grep/Glob` 只读能力，不因附带工具 schema 自动升级到 Bash/Write/Edit。
- **路由策略部分合并**：`handleRoutingPolicy` 使用 `map[string]json.RawMessage` 实现 PATCH 语义，仅更新请求中提供的字段。

---

## 2. 已知问题（活跃 Bug）

### 2.1 【高优先级】`/model` 切换导致误报「供应商和 .toml 不一致」

**现象**：用户在 Grok 对话栏使用 `/model` 切换模型后，UI 提示「供应商和 .toml 不一致」，且新模型无法正常使用。重启 grok_switch 后恢复正常。

**根因**：
- `/model` 命令由 Grok CLI 直接写入 `~/.grok/config.toml` 的 `[models].default`。
- Switch 的内存 `routing.Policy.Default` 未同步更新。
- `CurrentMatchesRouting`（`config/routing.go`）比较内存策略与磁盘 config.toml，发现不一致 → 报 drift。
- 重启时 `ApplyCurrentRouting` → `repairRoutingPolicy` 重新对齐，但这只在路由失效时触发；若新模型仍在同一供应商路由目录中，策略不会更新，重启「修复」实际是因为 Agent 子进程重启后重新读取了 config.toml。

**影响**：用户体验受损，每次 `/model` 后需重启。

**修复方向**：在 `currentRouting()` 或 `handleStatus` 中检测「仅 default 漂移且新值仍是有效路由」时，自动同步策略而非报 drift。可利用已有的部分合并语义（见 8.3）实现增量更新。

---

### 2.2 【高优先级】跨供应商子代理工具调用失败

**现象**：当子代理（explore/plan）指向与主对话不同的供应商 API 时，工具调用失败。

**根因**：
- 子代理继承主对话的工具 schema/协议（如 Responses API 的 `x_search`），但目标供应商可能是 Chat Completions 后端，不接受该 schema。
- `repairMalformedToolHistory` 仅处理空工具名，不处理协议不匹配。
- 跨供应商子代理的请求可能不经过订阅代理的修复层。

**影响**：explore/plan 子代理在跨供应商场景下不可用。

**已做**：新增 `browseruse_policy.go`，当子代理目标为非 Grok 模型时，通过 ACP `McpServers` 注入 browser-use MCP 工具替代原生 `x_search`。

**修复方向**：在子代理启动时，根据目标供应商的 `api_backend` 过滤/转换工具定义；或在请求层增加协议适配。

---

### 2.3 【中优先级】web_search 不支持 x_search 时仅提示，未自动切换

**现象**：当 `web_search` 指向 `chat_completions` 后端模型时，`WebSearchCapable = false`，UI 显示提示注释，但 Grok Agent 仍会尝试调用原生 `web_search`/`x_search` 工具并失败。

**根因**：
- `WebSearchCapable` 仅用于 UI 提示和 `SnippetForProfile` 注释。
- 没有机制阻止 Grok Agent 调用不支持的工具。
- 没有自动切换到 browser-use CLI 的通路。

**影响**：用户需要手动处理搜索失败。

**修复方向**：
1. 当 `WebSearchCapable = false` 时，不写入 `[models].web_search` 到 config.toml（禁用原生搜索）。
2. 通过 ACP `McpServers` 注入一个 `web_search` MCP 工具，后端调用 browser-use CLI。

---

### 2.4 【中优先级】路由策略与供应商 web_search/explore/plan 冲突

**现象**：供应商表单中有 `web_search_model`、`subagents_explore_model`、`subagents_plan_model`；路由视图中也有 `routingWebSearch`、`routingExplore`、`routingPlan`。两者容易冲突。

**根因**：
- 供应商级别的 `WebSearchModel` / `SubagentsModels` 在激活时写入 config.toml。
- 路由策略的 `WebSearch` / `Subagents` 在路由视图保存时写入 config.toml。
- 两者是同一组 TOML 键的不同写入源，后写者覆盖先写者。
- 用户在一个视图中修改后，另一个视图显示过期值。

**影响**：配置漂移，用户困惑。

**修复方向**：统一写入源——路由策略是唯一真相；供应商表单中的字段仅作为默认值，激活时合并到路由策略。

---

## 3. 技术债

### 3.1 代码层面
- **`server.go` 过大**：1300+ 行，包含所有 HTTP 处理器。应按资源拆分（`profiles.go`、`routing.go`、`agent.go` 已拆分，但 `server.go` 仍剩 1200+ 行）。
- **`handleConfig` PUT 无校验**：用户可直接写入任意 TOML 内容，可能破坏路由一致性。
- **`repairMalformedToolHistory` 是启发式清洗**：遍历整个 JSON 树查找空 name，对大请求有性能开销。
- **测试覆盖不均衡**：`config/`、`routing/`、`profiles/` 有较好覆盖；`server/` 集成测试不足。

### 3.2 架构层面
- **Agent 子进程与 HTTP 服务器耦合**：`Server` 结构体同时持有 `Agent` 和 `Switcher`，职责过重。
- **MCP 会话刷新尚不完整**：browser-use MCP 已可注入新建/加载的 ACP 会话，但路由策略改变后，已存在会话仍需明确重启或重载才能获得新 MCP 配置。
- **web_search 能力检测是静态的**：`SupportsBackendSearch` 在 Profile 编辑时手动设置，未自动探测。

### 3.3 文档层面
- 缺少公开的 `problems.md` 或 CHANGELOG。
- 设计文档分散在 `docs/design/`，无索引。

---

## 4. 环境依赖

| 依赖 | 版本 | 状态 |
|:---|:---|:---|
| Go | 1.26 | 锁定在 `go.mod` |
| macOS | 14+ | 主要开发/运行平台 |
| CLIProxyAPI | v7.2.94 | 内嵌二进制，需与 `build-macos.sh` 同步更新 |
| CodeBuddy | 外部 | 可选，`/opt/homebrew/bin/codebuddy` |
| Grok CLI | 外部 | 必需，`grok agent stdio` 可用 |

---

## 5. 监控与可观测性

- **日志**：`grok_switch.log`（应用）、`agent.log`（Agent 子进程 stderr）。
- **崩溃报告**：`crash.RecoverMainThread` 捕获 panic，写入日志并弹原生对话框。
- **健康检查**：`GET /api/status` 返回 `config_matches_active` / `config_matches_routing`。
- **无远程遥测**：`ApplyPrivacyProtection` 默认禁用 telemetry。
