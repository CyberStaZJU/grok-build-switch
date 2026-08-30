# Status.md — 项目当前状态（代理工作摘要）

> 权威细节见 [docs/status.md](docs/status.md)；本文件为代理快速上下文，保持精简并及时更新。
> 最后更新：2026-08-30

## 当前可用能力

- 官方 Grok CLI 登录与模型路由；普通 Profile 管理（供应商/URL/Key/格式/模型/上下文窗口）。
- 单一启用供应商路由（routing.json schema v2），路由切换事务化、失败回滚。
- 订阅代理：内嵌 CLIProxyAPI；ownership ledger + marker + 跨进程锁 + recovery journal；未知并发状态 fail closed。
- Max Collaboration（collaboration.json schema v5，四角色 Standard/Fast + effort）；apply 写入顺序 artifacts → config → routing → policy，后段失败补偿回滚。
- 用量观察（只读聚合 token 与缓存命中率，不推算成本）。
- 配置编辑、菜单栏/Wails 壳、LAN 配对、SSH 远程管理。

## 进行中 / 阻塞

- **发布阻塞**：本机缺少有效 `Developer ID Application` 身份与 notarytool profile；当前只能产出 ad-hoc 本地候选，不能声称已签名/已公证。
- 发布前重跑：最新全量 race 与独立 `check-work`。
- 五条 tier 的生产 renderer 脚本在 canned host 下未采用精确 budget，不能记为 path-specific PASS；仅完成一次 Economy 最小只读 live smoke。

## 技术债（要点）

- `server.go` 偏大，可继续按资源拆分。
- TOML 直编入口需持续加强与统一路由的一致性校验。
- `SupportsBackendSearch` 依赖 Profile 声明，无通用自动探测。
- 用量 per-turn 模型归属是近似值（按 session summary 归属），不可作精确计费证据。
- CLIProxy 上游管理端点无 ETag/CAS，无法与不合作外部写入者形成真正原子事务。

## 最近变更

- 2026-08-30：按用户决定暂不接入 `ultra`、只接入到 `max`。供应商编辑菜单与可信 Codex Standard/Fast 能力均移除 `ultra`；服务端过滤陈旧声明并明确拒绝绕过，Switch 内部 `reasoning_efforts_source` 不再写入 Grok `config.toml`。同时修复托管 Profile effort-only 保存因提前补齐已知上下文窗口而误报 HTTP 409，以及 Profile 规范化复用切片底层数组导致原元数据被修改的问题。前端 21 项、全量 Go、vet、相关包 race 与 Wails-tag 测试通过；Standard/Fast 的 `max` 均实际返回 `OK`，`ultra` 被 Grok Build `1.0.13` 拒绝。已构建并安装 `0.9.2 (build 11)` ad-hoc 候选，主程序 SHA-256 `65e5023dd283b6390a19534c03f20f05cc034fded41a3607219f6f48f09eb918`，与仓库构建副本一致，严格 codesign 校验通过但未使用 Developer ID 或 Apple 公证。真实桌面与 `390×844` 移动端首页、Codex 编辑、订阅代理和 CodeBuddy 页面通过；托管编辑器提交 `max` 成功且无横向溢出。验证后 Profile 恢复 Standard+Medium，全局路由恢复 Fast+High，`config.toml` 与 routing 一致，且不含 `ultra` 或 `reasoning_efforts_source`。浏览器录制位于 `~/.config/browser-harness/agent-workspace/recordings/gbs-max-only-final`。
- 2026-08-30：按用户要求只保留一个最新版 App。已删除仓库中被 Git 忽略的 `dist/macos/Grok Build Switch.app`（与当前安装同为 0.9.1 build 10 的构建副本），并删除 `~/.grok/build-state` 下三个经核验为 0.8.0/0.9.0 的旧 App bundle；父目录中的配置快照、清单和其他证据文件保留。Spotlight 全局应用索引现只返回 `/Applications/Grok Build Switch.app`；当前 0.9.1 build 10 的版本、主程序哈希和 `127.0.0.1:17878` 监听均正常。仓库中保留的 0.9.1 DMG 与 `.sha256` 是安装介质，不是第二个已安装 App。
- 2026-08-30：已停止并删除旧 `/Applications/Grok Build Switch.app`，从校验后的 DMG 安装并启动 `0.9.1 (build 10)`；本轮未保留被替换的 0.9.0 App 副本，运行配置、凭据和账号数据保持存在。当前 arm64 主程序 SHA-256 为 `be9d6590c1718fc4d37e23f9bf54f1ebdee28b4214500b5125f29b5cd1bd9a6c`，与 DMG 内主程序逐字节一致；App 为 ad-hoc 签名、未使用 Developer ID 或 Apple 公证，进程从安装路径运行并监听 `127.0.0.1:17878`。真实页面确认 CodeBuddy 当前只暴露 `hy4-preview` 与 `hy4-preview-x`、默认 `hy4-preview-x`，其他供应商模型仍存在，桌面与 `390×844` 移动布局通过。Finder 单独显示的时间不是版本依据；本次 bundle/binary 于 20:51–20:52 创建并在 20:54 安装。安装前配置快照和隔离验证状态保留在仓库外；该阶段的源码与文档改动当时尚未提交。
- 2026-08-30：CodeBuddy 页面新增“暴露给 Grok Build”模型多选，默认模型必须属于所选子集。后端只更新 CodeBuddy Profile 并通过统一路由重新组合所有供应商；隔离实例验证只选 `hy4-preview` 后 CodeBuddy 只贡献这一模型，另一个供应商及其模型保持不变，空子集被前后端拒绝，桌面流程与 `390×844` 移动布局通过。前端 21 项、全量 `go test ./...`、`go vet ./...`、CodeBuddy/Profile/Routing/Config/Server race 与 Wails-tag 测试通过。另确认 `max`/`ultra` 是 GPT 推理强度值而非独立模型；本机 Grok Build `1.0.13` 的 `/effort` 仅公开 low/medium/high/xhigh，因此它们不会作为 `/m` 模型项，且可能不出现在客户端档位选择界面。
- 2026-08-30：CodeBuddy 本机目录同步、Codex Sol Ultra、托管 Profile/路由边界加固、测试和文档已推送到 GitHub `CyberStaZJU/grok-build-switch` 的 `main`；功能提交为 `ef5d4522e75e322f08cc9a0e12cd217526050234`，已由本地 `HEAD`、`origin/main` 与远端 advertised ref 核验一致。提交不包含 `dist/`、`vendor/`、运行数据、凭据或私有路径。
- 2026-08-30：GitHub 同步前独立审查并修复四类边界：路由现在按具体模型声明校验推理强度，Terra/Luna Standard/Fast 及自定义显示名绕过均拒绝 `ultra`，Sol Standard/Fast 保持支持；供应商编辑器会保留已保存且由模型声明的 `low` 等非全局菜单值；CodeBuddy 本机目录跨文件按嵌入时间戳选择最新有效快照；CodeBuddy Profile 在后续路由/激活失败时恢复旧值或删除本次新建项。隔离源码实例已验证桌面 context-only 保存保留 `low`，移动端 `390×844` 无横向溢出；全量 Go、vet、race、前端 Node 与 Wails-tag 测试通过。
- 2026-08-30：已用重建的 DMG 替换并重启 `/Applications/Grok Build Switch.app`，当前为 `0.9.0 (build 9)`，arm64 主程序 SHA-256 `68e5de7a837dfded9fb08688b31858b093058850a58700ab6457098dee40423b`；严格 codesign 校验通过，但仍为 ad-hoc 签名、未使用 Developer ID 或 Apple 公证。真实订阅代理页面已显式更新 Codex 托管供应商，Sol Standard/Fast 均持久化声明 `ultra`；桌面编辑页保存 Standard+Ultra、受保护路由接口切换 Fast+Ultra、`390×844` 移动布局均通过，最后恢复原默认 Standard+Medium，API、`routing.json` 与 `config.toml` 一致。旧 App 回滚备份位于 `~/.grok/build-state/grok-build-switch-installed-backup-20260830T043532Z`；同步前配置快照位于 `~/.grok/build-state/grok-build-switch-live-ultra-sync-backup-20260830T044202Z`。
- 2026-08-30：Codex `gpt-5.6-sol` 的 Standard 与 Fast 路由现在都显式支持 `ultra` 推理档位；Terra/Luna 保持原档位集合。供应商编辑页全局菜单新增 `ultra`，订阅代理托管 Profile 可保存所选模型已声明的默认推理强度，同时继续只开放上下文窗口和默认推理强度，其他托管字段须走订阅代理流程。隔离实例已通过页面保存 Standard+Ultra、受保护路由 API 切换 Fast+Ultra、桌面/移动布局及 `config.toml`/`routing.json` 持久化验证；全量 Go、vet、受影响包 race、前端 Node 与 Wails-tag 测试通过。
- 2026-08-28：已构建并安装 `0.9.0 (build 9)` ad-hoc 本地候选到 `/Applications/Grok Build Switch.app`，旧 `0.8.0 (build 8)` 完整备份在仓库外 build-state。真实应用已同步 WorkBuddy 本机最新目录的 16 个 CodeBuddy 模型；CodeBuddy 保持未激活，当前默认供应商仍为 Codex `subscription/codex/gpt-5.6-sol`，`config.toml` 与 routing 一致。浏览器实测 `kimi-k3-2`、`hy4-preview`、`glm-5.3`、`glm-5.3-flash` 全部连通并返回 `pong`，订阅代理页面和桌面/移动布局回归通过。该安装不是 Developer ID 签名或 Apple 公证版本。
- 2026-08-28：CodeBuddy 页面新增最新模型目录接口与“刷新模型目录”操作。Switch 只读解析 WorkBuddy 本机已同步的 `cli` 模型目录，显示来源和更新时间，并可显式同步到托管 Profile；推理仍由 Switch 直接请求 `https://copilot.tencent.com/v2/chat/completions`，不经过 WorkBuddy agent harness。已直连验证 `kimi-k3-2`、`hy4-preview`、`glm-5.3`、`glm-5.3-flash` 返回 HTTP 200；隔离实例浏览器验证目录 16 模型、Kimi-K3 激活、GLM-5.3-Flash 连通以及桌面/移动布局通过。
- 2026-08-19：本机已构建并安装 `0.8.0 (build 8)` ad-hoc 本地候选，旧 `/Applications/Grok Build Switch.app` 已删除并替换；新版通过管理页面事务性保存 Codex GPT `context_window=320000`，保留已声明的 `low` 推理档位与 Standard/Fast 元数据，默认供应商恢复为 Kimi。该产物不是 Developer ID 签名或公证版本；浏览器、前端 Node、全量 Go、vet 与受影响包 race 验证通过。
- 2026-08-19：Codex/GPT-5.6 订阅代理默认上下文窗口由 272000 调整为 320000；供应商编辑页允许订阅代理 Profile 修改上下文窗口与已声明的默认推理强度，同时保留 Source 与 Standard/Fast 托管元数据，其他字段仍须通过订阅代理页面更新。前端 Node 测试、`go test ./...`、`go vet ./...`、受影响包 race 与隔离实例浏览器验证通过。
- 2026-08-19：发布前完成待提交代码与轻量文档的隐私审计；常见密钥格式与私有 home 路径扫描无发现，`dist/`、`vendor/`、本机配置和运行凭据保持忽略、不进入 Git。前端 Node 测试、`go test ./...`、`go vet ./...`、受影响包 race 与 Wails-tag 测试均通过。
- 2026-08-19：修复 CodeBuddy Profile 将 `@CodeBuddy / WorkBuddy` 显示后缀误作上游模型 ID、以及选择未启用默认模型时静默回退的问题；本机 Profile 已通过管理 API 规范化为 `hy3`、`deepseek-v4-flash`、`deepseek-v4-pro`。
- 2026-08-19：实测 CodeBuddy 的 `deepseek-v4-flash` 与 `deepseek-v4-pro` 网关上限为 1,048,576 tokens；1,048,538 prompt tokens 可接受，1,048,548 prompt tokens 被拒绝。配置窗口按用户决定设为 1,000,000，为系统提示、工具调用和输出保留余量；两者在约 1,039,948 prompt tokens 均可准确召回开头与中部标记。
- 2026-08-18：建立根目录 AGENTS.md / Status.md / Plan.md 代理协作文件。
- 2026-08-18（docs/status.md）：官方 default 保留全目录；详见权威状态文档。
