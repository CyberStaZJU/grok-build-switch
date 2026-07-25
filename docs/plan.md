# Grok Build Switch — Plan 文档

> 近期待办、中期方向与风险。最后更新：2026-07-24。

---

## 0. 本期已完成

| 项目 | 状态 | 关键文件 |
|:---|:---|:---|
| CodeBuddy 权限扩展（只读→全工具执行） | ✅ 完成 | `internal/codebuddy/runner.go`、`events.go`、`prompt.go`、`server/codebuddy.go` |
| 菜单栏常驻（macOS NSStatusItem） | ✅ 完成 | `gui_tray_darwin.go`、`gui_tray_darwin_provider.go`、`vendor/fyne.io/systray/systray_darwin.m` |
| 订阅代理状态/控制修复 | ✅ 完成 | `internal/cliproxy/adapter.go`、`internal/server/subscription_proxy.go` |
| stream_tool_calls 配置修复 | ✅ 完成 | `~/.grok/config.toml`（全局 false，按模型覆盖 true） |
| 路由策略部分合并语义 | ✅ 完成 | `internal/server/routing.go`（`map[string]json.RawMessage` 增量更新） |
| 跨供应商子代理 browser-use 注入 | ✅ 部分完成 | `internal/browseruse/mcp.go`、`internal/server/browseruse_policy.go` |
| Subagents `coding` 字段清理 | ✅ 完成 | 从 `SubagentsPolicy` 移除 `Coding` 字段，清理 10+ 文件引用 |
| `is_active` 概念移除 | ✅ 完成 | 路由策略作为唯一真相，所有 Profile 平等参与路由 |

---

## 1. 近期待办（本周）

### 1.1 修复 `/model` 切换漂移

**目标**：用户在对话栏使用 `/model` 后，Switch 自动同步策略，不报误报，模型立即可用。

**方案**：
1. 在 `currentRouting()`（`server/routing.go`）中增加「仅 default 漂移」检测：
   - 读取磁盘 config.toml 的 `[models].default`。
   - 若该值是路由目录中的有效路由，且与 `policy.Default` 不同，则自动更新策略。
2. 修改 `CurrentMatchesRouting` 或 `handleStatus`：区分「供应商漂移（真）」与「模型漂移（可自动修复）」。
3. 添加回归测试：模拟 `/model` 写入 config.toml，验证策略自动同步。

**涉及文件**：
- `internal/server/routing.go`
- `internal/config/routing.go`（`CurrentMatchesRouting`）
- `internal/server/routing_test.go`

---

### 1.2 web_search 自动切换到 browser-use（已部分完成）

**目标**：当 `WebSearchCapable = false` 时，禁用原生搜索，自动提供 browser-use 搜索。

**已完成**：
- `internal/browseruse/mcp.go` 实现，暴露 `web_search`/`web_fetch` 工具。
- `internal/server/browseruse_policy.go` 实现子代理的 MCP 注入逻辑。

**待完成**：
1. **禁用原生搜索**：`ProfileForRouting` / `ApplyProfile` 中，当 `WebSearchCapable = false` 时，不写入 `[models].web_search`。
2. **UI 提示**：路由视图中标注「此模型使用 browser-use 搜索」。
3. **主对话的 browser-use 注入**：当前仅子代理有 browser-use 注入，主对话仍需处理。

**涉及文件**：
- `internal/config/routing.go`（`ProfileForRouting`）
- `internal/agentbridge/bridge.go`（`NewSessionLocked`、`LoadSessionLocked`）
- `internal/server/routing.go`（`routingDTO` 增加 `web_search_backend` 字段）

---

### 1.3 统一 web_search / explore / plan 写入源

**目标**：消除供应商表单与路由视图的配置冲突。

**方案**：
1. 路由策略（`routing.RoutingPolicy`）作为唯一真相。
2. 供应商表单中的 `web_search_model`、`subagents_explore_model`、`subagents_plan_model` 仅作为「默认值」使用。
3. 激活 Profile 时，若路由策略中对应字段为空，则从 Profile 填充；若已有值，保留路由策略的值。
4. 路由视图保存时，不回写 Profile（避免循环）。
5. UI 提示：「模型路由已单独配置，修改请在路由视图中进行」。

**涉及文件**：
- `internal/server/routing.go`（`activateProfileRouting`、`handleRoutingPolicy`）
- `ui/app.js`（`saveRoutingPolicy`、`syncEnabledModelList`）

---

## 2. 中期方向（1-4 周）

### 2.1 跨供应商子代理工具协议适配（已部分完成）

**目标**：explore/plan 子代理在不同供应商间可靠工作。

**已完成**：`browseruse_policy.go` 在非 Grok 子代理目标时注入 browser-use MCP 替代 `x_search`。

**待完成**：
1. 主对话的 browser-use 注入（不仅限于子代理）。
2. 子代理启动时根据目标供应商 `api_backend` 过滤/转换完整工具定义（不仅是 web_search）。
3. 在 `repairMalformedToolHistory` 基础上增加「协议降级」层。

---

### 2.2 自动探测模型能力

**目标**：减少手动配置 `supports_backend_search`、`reasoning_efforts`。

**方案**：
1. 利用现有的 `handleReasoningEfforts` 探测机制，扩展为通用能力探测。
2. 新增 `GET /api/models/capabilities` 端点，探测：
   - 是否支持 `x_search`（responses + backend_search）
   - 是否支持 `reasoning_effort`
   - 上下文窗口大小
3. Profile 编辑时提供「自动探测」按钮。

---

### 2.3 代码拆分与测试补全

**目标**：降低 `server.go` 复杂度，提高测试覆盖。

**方案**：
1. 将 `handleConfig`、`handleFetchModels`、`handleReasoningEfforts` 拆到独立文件。
2. 为 `server.go` 中剩余处理器编写表驱动测试。
3. 增加端到端测试：模拟完整供应商切换流程。

---

### 2.4 subagents 路由可视化

**目标**：在路由视图中暴露 `[subagents.models]` 的 explore/plan 配置，避免用户困惑于 default 已变但子代理仍用旧模型。

**方案**：
1. 路由策略增加 `subagents_explore`、`subagents_plan` 字段。
2. 路由视图增加对应选择器。
3. 写入 config.toml 时映射到 `[subagents.models].explore/plan`。

---

## 3. 长期方向（1-3 月）

### 3.1 多平台支持
- Windows 已有部分支持（`process_windows.go`、`autostart_windows.go`），但 GUI 和托盘待完善。
- Linux 支持（无托盘，仅 HTTP 服务器模式）。

### 3.2 配置即代码
- 支持 `grok_switch.yaml` 声明式配置，便于版本控制和团队共享。
- 导入/导出配置包。

### 3.3 智能路由
- 根据任务类型（编码/搜索/规划）自动选择最优供应商和模型。
- 基于延迟/成本/成功率的反馈路由。

---

## 4. 风险

### 4.1 Grok CLI 协议变更
- **风险**：`grok agent stdio` 的 ACP 协议或 config.toml schema 变更，导致 Switch 写入无效配置。
- **缓解**：`PreviewRouting` 在写入前验证；`UseOfficialAuthText` 清理未知键；跟进 Grok 文档更新。

### 4.2 CLIProxyAPI 版本锁定
- **风险**：CLIProxyAPI 更新后，内嵌二进制不兼容。
- **缓解**：`build-macos.sh` 中 SHA256 校验；版本号硬编码在代码中，更新时同步修改。

### 4.3 macOS 签名与公证
- **风险**：无 `APPLE_SIGNING_IDENTITY` 时退化为 ad-hoc 签名，Gatekeeper 可能拦截。
- **缓解**：`build-macos.sh` 支持 `--require-signature` 强制要求签名；文档说明公证流程。

### 4.4 凭证安全
- **风险**：`profiles.json` 明文存储 API Key；`config.toml` 写入时若权限过大可能泄露。
- **缓解**：文件权限 `0600`；数据目录在 `~/Library/Application Support/`；不序列化凭证到 `routing.json`。

### 4.5 单 CLIProxyAPI 进程假设
- **风险**：若用户手动启动另一个 CLIProxyAPI 实例，端口冲突。
- **缓解**：`cliproxy.Manager` 检测现有进程；启动前检查端口占用。

---

## 5. 决策记录

### 5.1 为什么用路由层而非直接写 config.toml？
- 直接写 config.toml 无法处理多供应商共存（模型名冲突、供应商切换时的会话保持）。
- 路由层提供抽象：Profile → Routing → Hydrated Snapshot → config.toml。

### 5.2 为什么 session graph 用逻辑 ID？
- Grok CLI 的 session ID 是供应商原生的，跨供应商无法直接 resume。
- 逻辑 ID 作为稳定锚点，每个供应商分支独立管理。

### 5.3 为什么 CodeBuddy 从只读改为全工具执行？
- 原版设计假设 CodeBuddy CLI 不支持工具调用，但研究发现它支持 `--permission-mode` 和 `--tools` 参数。
- CodeBuddy 的 stream-json 格式包含完整的 tool_use/tool_result 事件流，可解析并透传。
- 「原生执行」模式让 CodeBuddy 自己调用工具（而非 Switch 转发），避免了协议不匹配。
- 工具执行摘要作为响应末尾附加文本返回，用户可见但不可执行（安全提示仍保留）。

### 5.4 为什么菜单栏常驻选择 systray 而非纯 cgo？
- 纯 cgo 方案（自写 Objective-C NSStatusItem）导致 `AppDelegate` 符号与 Wails 冲突。
- `fyne.io/systray` 的 `Register()` + `RunWithExternalLoop` 模式可与 Wails 的 Cocoa 主循环共存。
- 代价：需修改 vendored systray 的类名（`AppDelegate` → `SystrayAppDelegate`）以避免重复符号。

### 5.5 为什么路由策略采用部分合并而非全量替换？
- 菜单栏常驻窗口只需要更新 `default` 字段，不应覆盖 `web_search`/`explore/plan`。
- `map[string]json.RawMessage` 方案允许检测请求中实际存在的字段，仅合并这些字段。
- 全量替换要求客户端发送完整策略，增加出错概率和网络开销。

### 5.6 为什么 stream_tool_calls 需要按模型配置？
- 不同 API 端点的 streaming tool_call 实现质量不同。
- LongCat-2.0 和 codebuddy/* 的 streaming 格式可靠，可安全启用。
- gpt-5.6-sol@API池 等端点的 streaming chunk 可能丢失 `function.name`，必须关闭。
- 全局默认 `false`，按模型白名单启用 `true` 是最安全的策略。

### 5.7 为什么 CLIProxyAPI 账号管理改用直接文件操作？
- CLIProxyAPI v7.2.94 管理 API 不支持 PATCH（返回 404），无法通过 HTTP 接口修改账号。
- Auth 文件存储在 `~/Library/Application Support/Grok Build Switch/cliproxy/auth/`，格式简单。
- 直接 `os.Remove` 删除文件比 URL 编码的 DELETE 请求更可靠。

---

## 6. 参考

- 架构手册：`docs/agent.md`
- 状态与问题：`docs/status.md`
- 设计文档：`docs/design/session-load-notification-overflow.md`、`docs/design/subagents-config-fix.md`
- 用户文档：`README.md`、`docs/usage.md`
