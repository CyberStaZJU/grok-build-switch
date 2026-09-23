# Status.md — 项目当前状态（代理工作摘要）

> 权威细节见 [docs/status.md](docs/status.md)；本文件为代理快速上下文，保持精简并及时更新。
> 最后更新：2026-09-23

## 当前安装与生产状态

- `/Applications/Grok Build Switch.app` `0.9.15 (build 31)`，ad-hoc 签名（**未公证**），`codesign --verify --deep --strict` 通过；主程序 SHA-256 `eef83774…7eb75`。内置 CLIProxyAPI 7.3.9（提交 `61fdfc34…41d267c`）。管理服务 `17878` 健康，订阅代理 `8317` 正常；`config_matches_active` 与 `config_matches_routing` 均为 true。
- 2026-09-23 已按换装流程重新构建并安装：隔离 HOME 完成全量 `go test` 门禁，bundle 先落 `/tmp` 暂存并清理扩展属性后校验签名、版本与架构，再退出旧进程、移动旧 bundle、安装新包并启动，健康检查通过后按用户明确要求删除旧 bundle（**未保留备份**）。旧 `.outgoing` 与暂存目录均已删除。
- 上一版 `0.9.14 (build 30)` 的 App bundle 已删除；`dist/macos` 只保留 0.9.15 的 App、DMG 与 `.sha256`。配置、账号认证与订阅凭据未改动。

## 订阅代理模型能力自动探测（2026-09-23，0.9.15）

- **需求**：更新「订阅代理 · ChatGPT/Codex」模型时，自动测试 Fast 与推理强度，有效就加入选项；触发范围限定为「目录更新时探测新出现或档位未记录的模型」。
- **探测信号（实测确定）**：CLIProxyAPI 自己校验 `reasoning_effort`，非法值返回 HTTP 400 并附该模型允许档位（`level "x" not supported, valid levels: ...`）。该响应按模型区分（实测 `gpt-5.5` 为 `low/medium/high/xhigh`，`gpt-5.6-*` 与 `gpt-6-astra`/`gpt-6-luna` 另有 `max`）。此请求在推理前被拒，不消耗 completion token。无可用账号时返回 503，模型保持未判定并在下次目录更新重试。
- **Fast 的判定边界（重要）**：无法从响应证明 `service_tier: priority` 生效——已知可用的 `-fast` 别名（由 CLIProxy 注入 priority）与 Standard 同样回报 `service_tier: "default"`，且 `service_tier` 完全不被校验。按用户决策，Fast 以「代理接受并能解析该模型」为准开启。因此 Fast 只是可获得的最强信号，不代表已证明更快。
- **实现**：`internal/cliproxy/capability.go` 新增能力探测与 `cliproxy/capability.json` 记录；`internal/modelvariants/registry.go` 的静态可信名单之上增加实测覆盖层（Fast 名单 + 每模型档位），静态名单与 `gpt-6-astra` 的仅推理声明保持不回退。`ReconcileModels` 在目录更新时探测、持久化并发布能力；`WriteConfig` 与 `Models` 只发布已记录结果、不额外探测。单次探测总预算 45s、单模型 20s、每次最多 24 个模型，避免拖慢保存请求。
- **记录字段**：`fast`、`efforts`、`efforts_known`、`unavailable`、`probed_at`。`efforts_known` 区分「无档位」与「尚未测量」；`fast` 与 `efforts_known` 由生成路由与档位声明共同决定。
- **生产实测（0.9.15）**：真实目录探测写入 12 条记录（7 个 chat 模型，5 个 image 模型判为不可用）；随后为 7 个实测模型生成 `-fast` 别名并纳入 priority 规则，订阅 Profile 出现 `gpt-6-astra`、`gpt-6-luna` 的 Standard/Fast 配对与五档声明，`config.toml` 同步写入。真实请求：Standard 与 Fast 路由均 HTTP 200，`grok -m subscription/codex/gpt-6-luna-fast -p` 返回 `FASTOK`。
- **`gpt-6-sol` 当前不可用（阻塞，非本改动引入）**：该账号对 `gpt-6-sol` 返回 `model_not_found: The model "gpt-6-sol" does not exist or you do not have access to it.`，其后一律 503；多次复测一致。它已从可选目录消失，因此本轮不会获得 Fast 或档位，需先在账号/上游侧解决访问权限。
- **既有漂移（先于本改动）**：`cliproxy/config.yaml` 存在 3 条 priority 规则，其中两条重复；与 2026-09-20 安装前快照逐字一致，且本改动未触碰 `internal/cliproxy/config_merge.go`。待后续单独处理。
- **验证**：`go test ./...`（隔离 HOME）、`go vet ./...`、`go test -race` 相关包、`node --check`、32 项前端测试全部通过；新增能力探测单测与 server 侧 Standard/Fast + 档位生成测试。本机宿主污染的 CodeBuddy 两项既有失败在隔离 HOME 下通过。
- **CLIProxyAPI 版本**：仍固定 7.3.9；上游最新 v7.3.15（相关条目见 docs/status.md）。本轮未升级。
- 2026-09-20 已按并发安全换装流程重新构建并安装：构建使用隔离 HOME，先验证 `.incoming` bundle，再停止并等待旧进程退出，移动旧包后原子替换，启动新实例健康检查通过后重启订阅代理。安装过程使用外部原子锁，旧 bundle 已在新实例健康后删除，未留下 `.incoming` 或回退包。
- 旧 `0.9.12` 的 App bundle、DMG 和 `.sha256` 安装包，以及更早历史版本的 App/DMG/校验文件，均已按用户要求删除；配置快照、日志和运行数据未删除。
- 生产已为「API 池」启用流式保护：`base_url` → `http://127.0.0.1:17878/stream-guard/v1`，`upstream_base_url` = `http://api-pool.example.com:11303/v1`。
- 换装顺序：先落 `.incoming`，再把旧 bundle **移动**到 `.superseded`；确认新版运行后才卸载旧包。此次旧包已在健康检查通过后卸载，构建日志与安装记录位于 `~/.grok/build-state/grok-build-switch-install-20260920/`。

## API 池 `gpt-6-astra` 404（2026-09-20，已解决）

- 现象：会话 `01a0a341…` 报 `model_not_found: Model "gpt-6-astra" is not supported by any configured account in this group`。
- 原因已用当前生产凭据复核：直接请求原始上游 `http://api-pool.example.com:11303/v1` 的 `GET /models` 不包含 `gpt-6-astra`；直接 POST `/responses` 对该模型仍返回 404，而 `gpt-5.6-sol` 返回 200。经本地流式保护端点复测结果一致。因此 API 池当前确实不支持 Astra，不是保护层误报。
- 处理：通过 loopback 管理 API 将 API 池的 `available_models` 与模型定义移除 `gpt-6-astra`，默认保持 `gpt-5.6-sol`，并保留 `upstream_base_url`。变更前快照保存在 `~/.grok/build-state/grok-build-switch-model-fix-20260920/pre-remove-api-pool-gpt-6-astra.json`。
- 订阅代理是独立路由：其目录仍提供并选中 `subscription/codex/gpt-6-astra`。最小真实请求返回 HTTP 200，但响应的实际 `model` 字段为 `gpt-5.6-luna`，所以只能确认该订阅别名可被接受并成功返回，不能把它表述为物理 Astra 已被验证。
- 验证：API 池路由目录已不再显示 Astra，`config_matches_active=true`、`config_matches_routing=true`；API 池经保护端点使用 `gpt-5.6-sol` 返回完成响应。原会话若仍固定旧模型，需要在会话内执行 `/model gpt-5.6-sol`；该选择属于 Grok CLI 会话状态，Switch 无法远程改写既有会话。
- 后续可选项：让模型目录按上游 `/v1/models` 定期或保存前校验，防止第三方上游下线模型后再次留下陈旧选项；本次只处理已确认的生产故障。

## 当前可用能力

- 官方 Grok CLI 登录与模型路由；普通 Profile 管理（供应商/URL/Key/格式/模型/上下文窗口/模型级推理强度声明）。
- 单一启用供应商路由（`routing.json` schema v2），切换事务化、失败回滚。
- 订阅代理：内嵌 CLIProxyAPI 7.3.9，Codex/Google 被动额度观测与同厂商聚合；Codex 模型在目录更新时自动探测推理档位与 Fast 可用性（见上节）。
- Max Collaboration（`collaboration.json` schema v5，四角色 Standard/Fast + effort）。
- 用量观察（只读聚合 token 与缓存命中率，不推算成本）。
- 流式保护（过滤自定义上游非标准 SSE 帧）；配置编辑、菜单栏/Wails 壳、LAN/SSH 远程管理。

## 进行中 / 阻塞

- **发布阻塞**：本机缺少有效 `Developer ID Application` 身份与 notarytool profile；当前为 ad-hoc 本地候选，不能声称已正式签名/已公证。

## 技术债（要点）

- `server.go` 偏大，可继续按资源拆分。
- TOML 直编入口需持续加强与统一路由的一致性校验。
- `SupportsBackendSearch` 与模型级推理强度都依赖 Profile 声明，无通用自动探测。
- 用量 per-turn 模型归属是近似值（按 session summary 归属），不可作精确计费证据。
- CLIProxy 上游管理端点无 ETag/CAS，无法与外部写入者形成真正原子事务。
- 多个 `grok` 会话共用同一份 `~/.grok/config.toml`，各自 `/model` 选择会互相覆盖 `[models].default`，Switch 侧写入不具排他性。`config_matches_active=false` 因此既可能来自并发写入、也可能是不一致，须结合 `[model.*]` 路由表判断。
- `profileMutationModelDTO` 不含 `stream_tool_calls`：普通 Profile 模型若带该字段，经供应商编辑页保存会丢字段（托管 Profile 走 `previous` 重建路径，不受影响）。仅代码路径确认，未端到端复现。
- Fast 无法从响应证明：`service_tier` 不被 CLIProxy 校验，Standard 与 Fast 都回报 `default`。当前 Fast 以「代理接受该模型」为准开启，属可获得的最强信号；若上游忽略 priority，用户会以为更快而实际无变化。
- `cliproxy/config.yaml` 存在 2 条重复的 priority 规则（先于本改动的既有漂移，与 2026-09-20 快照逐字一致）；`mergeManagedFastRule` 只按 fingerprint 移除受管规则，未清理历史重复项。

## 最近变更

- 2026-09-23：实现订阅代理 Codex 模型能力自动探测（`internal/cliproxy/capability.go` + `internal/modelvariants` 实测覆盖层），目录更新时为新模型自动判定推理档位与 Fast；构建并安装 `0.9.15 (build 31)`（ad-hoc，未公证），生产实测生成 7 条 Fast 别名并写入档位声明，真实请求与 `grok` CLI 路由均通过；按用户要求删除旧 bundle 且**未保留备份**。
- 2026-09-23：核对 CLIProxyAPI 版本：本机固定 7.3.9，上游最新 v7.3.15；未升级（无故障证据，升级属独立发布动作）。
- 2026-09-20：升级内嵌 CLIProxyAPI 到 `7.3.9`（提交 `61fdfc34…41d267c`），重新构建并安装 Grok Build Switch `0.9.14 (build 30)`；全量 Go 测试和构建门禁通过，生产订阅代理重启后健康检查通过，账号与模型目录保留。
- 2026-09-20：重新构建并安装 `0.9.13 (build 29)`，包含 `upstream_base_url` 编辑往返修复；通过外部安装锁、`.incoming` 预校验、旧进程等待退出、健康检查后卸载旧包的并发安全换装流程。
- 2026-09-20：修复 `PUT /api/profiles/{id}` 丢弃 `upstream_base_url`（编辑页保存会让流式保护失去上游 → 全部请求 503）；补经 handler 驱动的回归测试，生产已恢复并复验。
- 2026-09-20：定位并处理 API 池 `gpt-6-astra` 404 —— 原始上游不提供 Astra，已从 API 池模型目录移除；订阅代理别名保留并单独记录实际响应模型。
- 2026-09-19：构建并安装 `0.9.12 (build 28)`，含流式保护（过滤自定义上游非标准 SSE 帧 + 回填 `response.failed` 缺失字段）；生产已为「API 池」启用。安装验证中发现并修复开关的 `routingMu` 重入死锁，补 HTTP 路由级回归测试。真实池 `keepalive` 帧与紧随的 `response.failed` 已固化为测试夹具。
- 2026-09-18：修复自定义上游下会话标题固定请求内置 `grok-4.6` 导致 404（启用供应商时一并写入 `[models].session_summary`）；连接测试不再把网关 HTML 首页的 200 当成功。安装 `0.9.11 (build 27)`。
- 2026-09-15：修复 CodeBuddy 托管 Profile 编辑页必然 409（未设置档位与 `none` 视为同值）；模型卡片支持按模型声明推理强度档位，DeepSeek `deepseek-flash` 声明五档。安装 `0.9.10 (build 26)` / `0.9.9 (build 25)`。
- 2026-09-13：GPT `gpt-5.6-sol`（含 Fast）与 `gpt-6-astra` 上下文窗口统一为 320000；安装 `0.9.8 (build 24)`。
- 更早：`0.9.7 build 23` 撤回 Codex 客户端入口；`0.9.5 build 19` 升级 CLIProxyAPI 7.2.152 并支持 gpt-6-astra / gemini-3.7/3.8；`0.9.4 build 18` 回退为 Grok 专用；`0.9.3 build 12` CodeBuddy 工具调用 ID 兼容；`0.9.2 build 11` Sol Ultra 档位与 CodeBuddy 模型多选。
