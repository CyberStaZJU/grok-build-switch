# Grok Build Switch — Status 文档

> 当前状态、产品边界与技术债。最后更新：2026-09-23（订阅代理 Codex 模型能力自动探测，安装 0.9.15）。

---

## 订阅代理模型能力自动探测（2026-09-23）

- **需求**：更新「订阅代理 · ChatGPT/Codex」模型时自动测试 Fast 与推理强度，有效即加入选项；触发范围限定为「目录更新时探测新出现或档位未记录的模型」（用户决策）。
- **探测信号**：CLIProxyAPI 自身在校验 `reasoning_effort`。传入非法值时它返回 HTTP 400，并在消息中列出该模型接受的档位：`level "x" not supported, valid levels: low, medium, high, xhigh, max`。该列表按模型区分——实测 `gpt-5.5` 为 `low/medium/high/xhigh`，`gpt-5.6-terra/sol/luna`、`gpt-6-astra`、`gpt-6-luna` 另有 `max`。请求在推理前即被拒绝，因此不消耗 completion token，也不需要预先存在别名（可直接用物理模型 ID 探测）。
- **Fast 的判定边界（必须如实表述）**：没有任何响应字段能证明 `service_tier: priority` 生效。已知可用的 `-fast` 别名（由 CLIProxy 的 payload override 注入 priority）与 Standard 一样回报 `service_tier: "default"`；并且 `service_tier` 完全不被校验（传 `bogus-tier` 仍返回 200）。按用户决策，Fast 以「代理接受并能解析该模型」为准开启，这是可获得的最强信号，**不等于已证明更快**；上游若忽略 priority，用户会以为更快而实际无变化。
- **不可用与重试**：模型无可用账号时返回 503（`auth_unavailable` / `model_not_found`），记为 `unavailable` 且不授予能力，下次目录更新重试。返回「仅支持 /v1/images/*」等端点不匹配的模型记为已测量但无档位，不再重复探测。
- **实现**：
  - `internal/cliproxy/capability.go`：单请求探测、`valid levels` 解析、`capability.json` 记录（含 `version`，未知版本 fail closed）、启动与 reconcile 时的发布逻辑。
  - `internal/modelvariants/registry.go`：在静态可信名单（`gpt-5.6-terra/sol/luna`）之上增加实测覆盖层（Fast 名单 + 每模型档位）；静态名单与 `gpt-6-astra` 的「仅推理、不生成 Fast」声明不回退。
  - `ReconcileModels` 在目录更新时探测、持久化、发布；`WriteConfig` 与只读的 `Models` 只发布已记录结果、不发起探测。
  - 边界：单模型 20s、单次探测总预算 45s、每次最多 24 个模型，避免拖慢「保存模型选择」请求；发布顺序必须在读取所有权账本之前，否则校验探测得到的 Fast 别名会失败。
  - 稳健性：`publishCapabilities` 同时从 `config-ownership.json` 恢复已拥有的 Fast 模型，因此 `capability.json` 丢失时降级为「有 Fast、无档位」，不会因别名校验失败而阻断启动。
- **生产实测（0.9.15）**：真实目录探测写入 12 条记录（7 个 chat 模型；5 个 image 模型判为不可用）；随后为 7 个实测模型生成 `-fast` 别名并纳入 priority 规则，订阅 Profile 生成 `gpt-6-astra`、`gpt-6-luna` 的 Standard/Fast 配对与五档 `reasoning_efforts`，`config.toml` 同步写入。真实请求：Standard 与 Fast 路由均返回 HTTP 200；`grok -m subscription/codex/gpt-6-luna-fast -p ...` 返回 `FASTOK`。`config_matches_active` 与 `config_matches_routing` 均为 true。
- **`gpt-6-sol` 当前不可用（阻塞，非本改动引入）**：当前启用的 plus 账号对 `gpt-6-sol` 返回 `model_not_found: The model "gpt-6-sol" does not exist or you do not have access to it.`，其后一律 503（chat 与 responses 两条协议、多次复测一致）。该模型已从可选目录消失，因此本轮不会获得 Fast 或档位；需先在账号/上游侧解决访问权限。会话早期一次探测曾连续 7 次返回 200，无法解释，不排除当时命中另一账号。
- **既有漂移（先于本改动）**：`cliproxy/config.yaml` 存在 3 条 priority 规则，其中两条重复。与 2026-09-20 安装前快照逐字一致，且本改动未触碰 `internal/cliproxy/config_merge.go`；`mergeManagedFastRule` 只按 fingerprint 移除受管规则，不清理历史重复项。
- **验证**：隔离 HOME 下 `go test ./...` 全绿、`go vet ./...` 通过、`go test -race`（cliproxy/modelvariants/server）通过、`node --check ui/app.js` 通过、32 项前端测试通过；新增能力探测单测（探测授予、503 重试、已测量不重测、账本往返与启动顺序、未知版本 fail closed、静态名单不回退）与 server 侧「实测模型生成 Standard/Fast + 档位」测试。本机宿主污染的 CodeBuddy 两项既有失败在隔离 HOME 下通过。
- **CLIProxyAPI 版本**：仍固定 7.3.9（提交 `61fdfc34…`）；上游最新 v7.3.15。相关条目：7.3.15 `fix(codex): compact client model catalog and preserve required fields`、`feat(registry): add grok-4.7-build-fast`；7.3.14 `feat(registry): add gpt-6-luna to codex-free`；7.3.13 registry 更新与 codex client 0.155.0；7.3.11 `feat(pluginapi): propagate ... service tier`、`fix(responses): filter upstream private and telemetry events in SSE streams`；7.3.10 `fix(translator): preserve reasoning content across tool turns`。无故障证据，升级属独立发布动作，待用户决定。

## 当前生产安装（2026-09-23）

当前安装为 `/Applications/Grok Build Switch.app` `0.9.15 (build 31)`，ad-hoc 签名（未公证），`codesign --verify --deep --strict` 通过；主程序 SHA-256 `eef837748dc0bd4d8940337ec749110a82a9c3c3569f8d42ac2d08a36e47eb75`。内置 CLIProxyAPI 7.3.9（提交 `61fdfc341b96178a8dcb53f2efc46cbc341d267c`）。管理服务 17878 健康，订阅代理 8317 正常。

换装流程：隔离 HOME 完成全量 `go test` 门禁后构建；bundle 先复制到 `/tmp` 暂存并 `xattr -cr` 清理扩展属性（仓库位于 Documents，File Provider 会给 bundle 加 `com.apple.FinderInfo`，导致仓库内签名校验失败），随后校验签名、版本与 arm64 架构；退出旧进程并等待其结束，把旧 bundle 移动为 `.outgoing`，安装新包并启动，健康检查与管理/代理状态核对通过。按用户明确要求，本次**未保留备份**：`.outgoing` 与 `/tmp` 暂存目录已删除，旧 `0.9.14` 的 App bundle 与 `dist/macos` 中 0.9.13 的 DMG/校验文件一并删除；配置、账号认证与订阅凭据未改动。


## API 池 `gpt-6-astra` 404（2026-09-20，已解决）

- **现象**：会话 `01a0a341…` 报 `Not found (404): model_not_found: Model "gpt-6-astra" is not supported by any configured account in this group`。
- **上游复核**：使用当前 API 池凭据直接请求原始上游 `http://api-pool.example.com:11303/v1`，`GET /models` 不包含 `gpt-6-astra`；`POST /responses` 对该模型返回同一 404，对 `gpt-5.6-sol` 返回 200。经本地流式保护端点复测结果一致，故不是保护层误报。
- **处理**：通过 loopback 管理 API 将 API 池 Profile 的 `available_models` 和模型定义移除 `gpt-6-astra`，默认保持 `gpt-5.6-sol`，显式保留 `upstream_base_url = http://api-pool.example.com:11303/v1`。变更前快照保存在 `~/.grok/build-state/grok-build-switch-model-fix-20260920/pre-remove-api-pool-gpt-6-astra.json`。
- **订阅代理边界**：订阅代理目录仍提供并选中 `subscription/codex/gpt-6-astra`。最小真实请求返回 HTTP 200，但返回 JSON 的 `model` 字段为 `gpt-5.6-luna`；因此当前证据只证明该订阅别名可接受并返回结果，不能声称物理 Astra 已验证。
- **验证**：API 池路由目录不再显示 Astra，`config_matches_active=true`、`config_matches_routing=true`；API 池通过保护端点使用 `gpt-5.6-sol` 返回完成响应。原会话若仍固定旧模型，需要在会话内执行 `/model gpt-5.6-sol`；Switch 无法远程改写既有 Grok CLI 会话的模型状态。
- **后续可选项**：让模型目录按上游 `/v1/models` 定期或保存前校验，避免第三方上游下线模型后留下陈旧选项。本次只处理已确认的生产故障。

## 当前生产安装（2026-09-20）

当前安装为 `/Applications/Grok Build Switch.app` `0.9.14 (build 30)`，ad-hoc 签名（未公证），`codesign --verify --deep --strict` 通过；主程序 SHA-256 `a77774418b3564adb3044bfeb4585dbe9ec4cbd3de4edf6b785d2b129ea00fe9`。内置 CLIProxyAPI 7.3.9（提交 `61fdfc341b96178a8dcb53f2efc46cbc341d267c`）。管理服务 17878 健康，Grok 订阅代理 8317 正常。

本版包含 `upstream_base_url` 编辑往返修复和 API 池模型目录清理。生产已为「API 池」供应商启用流式保护：`profiles.json` 中该 Profile 的 `base_url` 指向 `http://127.0.0.1:17878/stream-guard/v1`，真实上游保存在 `upstream_base_url = http://api-pool.example.com:11303/v1`；API 池默认模型为 `gpt-5.6-sol`，`gpt-6-astra` 已从该 Profile 和路由目录移除；`config_matches_active` 与 `config_matches_routing` 均为 true。

换装采用外部安装锁与事务顺序：构建使用隔离 HOME；先把 bundle 复制到 `/Applications/Grok Build Switch.app.incoming` 并完成签名、版本和架构校验，再停止并等待旧进程退出，将旧 bundle 移动到临时回退位置，原子移动新 bundle，启动后检查管理服务、配置一致性和真实模型请求，健康检查通过后才删除旧 bundle。本次升级保留了配置、账号认证和订阅凭据；旧回退 bundle 已在新实例健康后删除，`.incoming` 与安装锁无残留。升级构建日志与安装前快照位于 `~/.grok/build-state/grok-build-switch-cliproxy-739-install-20260920/`。

## 流式保护：自定义上游的非标准 SSE 帧（2026-09-19）

- **问题**：经 API 池供应商推理时整轮失败，Grok Build 报 `serialization error: unknown variant \`keepalive\`, expected one of \`response.created\`, ... at line 1 column 19`。该路径 `is_retryable: false`，重发同样失败。
- **根因**：网关（自报 `303Lab - AI API Gateway`）在静默期注入自定义 SSE 帧 `event: keepalive` / `data: {"type":"keepalive","sequence_number":2}`。Grok 客户端把 Responses 事件的 `type` 反序列化为**封闭枚举**（49 个变体，已逐一比对二进制与运行时错误信息，完全一致），未知变体即终止该轮。与额度、鉴权、网络无关。
- **真实帧已抓取**（2026-09-19，本机透明转发代理记录真实池流量）：心跳帧如上。**同一序列紧随其后是 `response.failed`**，其真实原因是 `{"code":"server_error","message":"Our servers are currently overloaded. Please try again later."}`——即上游瞬时过载。该 `response.failed` 帧**自身也缺失 schema 必需字段 `output`**，仅修掉心跳后客户端会立刻改报 `serialization error: missing field \`output\``。两个缺陷必须一起处理，否则错误仍会把「可重试的过载」伪装成不可重试的协议错误。
- **同时影响两条协议**：同一个心跳帧在 Chat Completions 路径也会失败，报错不同（`missing field \`id\``）。因此保护按协议分模式过滤。
- **修复**：新增 `internal/streamguard` 包，按帧（`field: value` 行 + 空行）解析 SSE：Responses 模式下丢弃 `type` 落在已知枚举之外的整帧，并对 `response.failed` 回填缺失的必需成员（`output` / `parallel_tool_calls` / `tool_choice` / `tools`，仅回填缺失项，绝不覆盖上游已有值，且保留 `sequence_number` 等无关成员）；Chat 模式下丢弃心跳与误入的 Responses 事件帧。合规帧逐字节透传，`data: [DONE]`、`{"error":{...}}`、无法解析的 JSON 与控制帧一律保留。`internal/server/stream_guard.go` 提供 loopback-only 转发端点 `/stream-guard/v1/*`；`/v1/models` 本地应答。
- **启用方式**：`PUT /api/stream-guard {"profile_id":"...","enabled":true}` 把该 Profile 的 Base URL 指向保护端点，并把原地址存入新增的 `profiles.Profile.UpstreamBaseURL`；关闭时还原并清空。该字段已加入 `profileMutationDTO`，供应商编辑页保存不会丢失。官方账号启用时返回 409。未启用时请求路径与转发结果完全不变。
- **顺带修复**：`internal/codebuddy/proxy.go` 的 `finish_reason` 归一化原先只处理 `""`，现同时把 `"error"` 归一为 `null`（CodeBuddy `hy4-preview` 会返回该值，撞上 `stop/length/tool_calls/content_filter/function_call` 这个封闭枚举）。
- **验证**：
  - 单元：`internal/streamguard` 覆盖未知事件丢帧、合规流逐字节透传、`response.failed` 回填、`sequence_number` 保留、合规 failed 帧不被改写、末尾无空行、读写错误传播、Chat 模式心跳与错误帧保留；`internal/server` 覆盖转发路径、凭据、模型改写、非 loopback 拒绝、上游 4xx/5xx 透传、本地模型目录、启用/关闭往返、编辑页往返、路由注册。
  - **真实帧回放（决定性证据）**：把抓取到的真实字节原样重放。未经保护时真实 `grok-1.0.34` 逐字复现 `unknown variant \`keepalive\``；经保护端点后 **0 个 serialization 错误**，客户端改为按瞬时故障重试。该真实帧已固化为测试夹具（`internal/streamguard/realcapture_test.go`、`internal/server/stream_guard_test.go`）。
  - 真实上游：保护端点对接真实 API 池，`grok -m gpt-5.6-sol` 简单请求与 `--reasoning-effort xhigh` 长流式请求均正常完成。
  - 门禁：隔离 HOME 下 `go test ./...` 全绿；`go test -race`（server/streamguard/profiles/routing/codebuddy）通过；`go vet` 通过。
  - **安装后生产端到端**：`grok -m gpt-5.6-sol`（经已启用的保护）返回预期文本；未受保护的 DeepSeek 官方与 CodeBuddy `hy4-preview` 两个供应商分别返回预期结果（回归确认）；关闭保护后 `config.toml` 的 `models_base_url` 与 Profile 同步还原为上游地址，重新启用后再次端到端通过。
- **开关的死锁修复（安装验证中发现）**：`handleStreamGuardSettings` 先取 `routingMu`，随后调用的 `SetStreamGuardEnabledOpt` 内部又走 `ApplyCurrentRouting()`，而后者会再次获取同一把互斥锁——Go 的 `sync.Mutex` 不可重入，请求永久挂起，且此后所有管理 API 全部阻塞（实测生产挂死、`/api/status` 超时）。更糟的是它只完成了 `Profiles.Update` 的一半：Profile 已指向保护端点，但路由投影未执行，`config.toml` 仍指向上游，形成前后不一致的中间态。
  - 修复：拆出不取锁的 `setStreamGuardEnabledLocked`，由持锁的调用方使用 `applyCurrentRoutingLocked()` 重新投影；HTTP handler 复用同一把锁下的内部方法，不再嵌套获取。
  - 回归测试：新增 `TestStreamGuardToggleOverHTTPDoesNotDeadlock`，**经注册的 HTTP 路由**驱动开关（此前的测试直接调方法，因此完全测不到这个死锁），并断言切换后服务器仍能响应。已验证该测试在旧代码上确实失败（`PUT /api/stream-guard deadlocked`）、在修复后通过。
- **边界**：只处理流式（`text/event-stream`）响应；非流式响应直接透传。保护端点仅接受 loopback 请求，不新增对外暴露面。不改变路由、凭据或模型目录的归属语义。保护只消除「帧导致的伪协议错误」，**不掩盖上游真实故障**——上游过载仍会如实传到客户端并可重试。已构建安装为 `0.9.12 (build 28)`。

## 当前生产安装（2026-09-18）

当前安装为 `/Applications/Grok Build Switch.app` `0.9.11 (build 27)`，ad-hoc 签名（未公证），`codesign --verify --deep --strict` 通过；主程序 SHA-256 `2ad2012046b0a42ee6a9b49f8fdda9ec91af54092e420ffc17c17f5024dbdc5d`，与本机 arm64 构建产物一致。内置 CLIProxyAPI 7.3.9。管理服务 17878 健康，Grok 订阅代理 8317 未受影响；升级未改动生产 Profile、routing 或订阅凭据。

换装流程改为：先把新包 `ditto` 到 `/Applications/Grok Build Switch.app.incoming`，再由独立脚本移动 bundle 并立即重启。运行中的进程在 bundle 被移走后仍从内存镜像服务，因此中断只来自重启本身（本次实测约 2 秒）。产物、DMG、`.sha256` 与 0.9.10 build 26 回退副本位于 `~/.grok/build-state/grok-build-switch-apipool-20260918/`，改动前的 `config.toml`/`profiles.json`/`routing.json` 快照在同目录 `prechange-config-snapshot/`。

构建脚本内建 `go test` 门禁仍受宿主 WorkBuddy 目录状态污染（`TestCodeBuddyProxyModelsLoopback`、`TestManagedCodeBuddyProfileRejectsOrdinaryProfileMutation`）；隔离 HOME 下两项通过。构建改用隔离 HOME 并复用真实 module/Go 构建缓存完成。

## 自定义上游的辅助模型固定（2026-09-18）

- **问题**：把 Grok Build 指向自定义上游（API 池，`http://api-pool.example.com:11303`，`openai_responses`）后，会话可以推理，但标题生成必然失败。日志证据：`model_id="grok-4.6" status_code=404`，报文为 `Model "grok-4.6" is not available for this group`。
- **根因**：Grok Build 用 `[models].session_summary` 选择生成会话标题/摘要的模型。该键未设置时它解析为内置 Grok 模型（当前 `grok-4.6`），而第三方网关只提供自己的模型清单，因此返回 404。主推理流程不受影响，所以这个缺口不会以明显报错暴露。
- **修复**：`internal/config/tomlio.go` 新增 `auxTitleModel`，在写 `[models]` 时一并写入 `session_summary = <供应商默认模型>`，覆盖 `ApplyProfile`、`ApplyProfileText`（`rewriteSection`，含丢弃未设置键的分支）与 `SnippetForProfile` 三条投影路径；`UseOfficialAuthText` 在切回官方账号时清除该键。因此换供应商会改指新供应商默认模型，不会残留指向上一家上游。
- **验证**：隔离实例中同一场景由 1–3 个 `grok-4.6` 404 变为 0 个 404；`grok -m gpt-5.6-sol` 真实完成一次写文件任务。生产实例（升级后）同样 0 个 404。
- **边界**：只固定 `session_summary`。实测 `image_description`、`prompt_suggestion`、`web_search` 在同一场景下不会退回到内置模型，因此未被接管。

## 上游连接测试的 HTML 误报（2026-09-18）

- **问题**：`probeModel` 把任何 2xx 视为连接成功。网关对未实现的协议路径会返回自己的 HTML 首页并带 HTTP 200（API 池在根路径 `/chat/completions` 即如此），于是「测试上游连接」会把一个永远无法推理的配置报成连通。
- **修复**：`probeModel` 读取响应体，识别 HTML 文档头即判失败，并提示改用该网关支持的协议或把 `/v1` 补进 Base URL。检查只拒绝 HTML，不放宽 JSON 网关、`messages` 兼容网关或 204 空响应。
- **实测协议差异**：API 池在 `/v1` 下 Responses 与 Chat Completions 均可用；在根路径只有 Responses 可用（Chat 会拿到 HTML，真实推理时挂住）。该池接受 `reasoning_effort` 的 `minimal/low/medium/high/xhigh/max`，拒绝 `ultra`——与 Switch 既有的 max-only 边界一致。

## 生产安装与更新（2026-09-13）

当前安装为 `/Applications/Grok Build Switch.app` `0.9.10 (build 26)`，ad-hoc 签名（未公证），`codesign --verify --deep --strict` 通过；内置 CLIProxyAPI 7.2.152。管理服务 17878 健康，Grok 订阅代理 8317 未受影响。DMG、校验文件、两个安装副本与覆盖前的 `0.9.8 (build 24)`、`0.9.9 (build 25)` 回退副本位于 `~/.grok/build-state/grok-build-switch-reasoning-effort-20260915/`。构建脚本在仓库内签名仍受 Documents File Provider 的 FinderInfo 竞态影响，改为在 `/tmp` 暂存目录清理扩展属性并签名后再安装。

本版包含模型级推理强度声明（见 1.1「模型级推理强度声明」，已用于 DeepSeek 官方供应商）与托管 Profile 编辑页 409 修复（见 1.1「托管 Profile 编辑页往返」）。

## 当前生产安装与客户端范围（2026-09-08）

按用户明确指令撤回 Codex 客户端入口，当时安装 `0.9.7 build 23`（ad-hoc 签名，未公证）。Grok 官方路由、普通供应商、WorkBuddy、GPT/Google 订阅代理、额度观察、配置编辑、LAN/SSH 保留。当前源代码不再含 Codex 专用页面、接口、目录同步或客户端配置写入；不把订阅来源名称中的 Codex 误当作客户端适配。

生产管理服务 17878、Grok 订阅代理 8317 正常；Codex 专用 8318 已停止，自启动项已移出 LaunchAgents。原 Codex 专用接口均返回 404。Codex 主配置只移除生成的 Switch 供应商表，其余配置语义保持；当前官方模型为 gpt-6-astra。Codex 未重启、已有任务未改写。

清理前后字节比对确认 Grok config、Profiles、应用设置、订阅配置/模型选择及所有现有订阅认证、Grok/Codex 官方认证保持；routing 只更新启动时间戳。旧版 App、专用适配器状态与故障日志、独立客户端配置以及恢复清单位于 `~/.grok/build-state/grok-build-switch-remove-codex-20260908/`；当前任务不再继续热切换修复。其他旧实验资料未扩大清理。

验证：全量 Go、vet、关键路由/代理 race、Wails 入口和 28 项前端测试通过。使用临时虚构供应商完成搜索、编辑保存及桌面/390×844移动端各页导航；设置页长配置路径溢出修复并复测。生产首页、订阅与 WorkBuddy 页面和核心只读 API 通过；未发起真实模型推理。最终安装包位于 `/private/tmp/gbs-grok-only-build-20260908-b23-final/`。

## 1. 当前状态

### 1.1 保留并维护的能力

- **2026-09-05 本轮外部资产已清理**：经用户授权删除 `~/.grok/build-state/grok-build-switch-astra-20260905/`（约 350 MB），包含回退 App、全部构建候选及安装介质、配置快照和隔离状态。删除前无进程占用，删除后路径不存在；当前 `/Applications/Grok Build Switch.app` 0.9.4 build 15 哈希未变、签名通过、管理页面 HTTP 200，运行配置与凭据未删除。

- **2026-09-05 Astra修复资格（当前安装见顶部回退说明）**：Astra阶段安装为 0.9.4 build 15，SHA-256 `74a2dd1b8e44b0724ad077bee6fb7801376b949d44fe0c928a1bd79369f9df8a`，ad-hoc 签名、未公证。Astra 明确声明 low/medium/high/xhigh/max，默认 medium，窗口 272000；不生成 Fast。声明 low 的模型可在编辑器主动选择 low，不再仅保留已保存值。全量 Go、vet、相关 race、前端 22 项通过；真实页面保存 low、移动编辑恢复 medium、再同步均核验，low/medium/max 最小真实请求返回 OK。CLIProxy 7.2.94 的 `usage-statistics-enabled` 已开启且后续合并保持 true；旧 `/usage` 不存在，`/api-key-usage` 返回空对象，上游 usage queue 为短期内存且读取会消费记录，因此本次未取走未知记录、未声称历史计费统计完成。最新 7.2.151 仍沿用此接口，无必要升级证据，暂未升级。设置页用量表移动溢出与重启后 CodeBuddy 子集扩张另记技术债；CodeBuddy 已恢复原单模型。完整本轮边界与外部保留资产见根 Status.md。

- **CodeBuddy 工具调用协议修复（已安装）**：2026-09-01 根据真实 Grok Build 会话确认，CodeBuddy 模型可能在多工具调用中返回空或缺失的 `tool_call.id`；首轮工具仍会执行，但携带空 `tool_call_id` 的下一轮请求会被上游以 HTTP 400 `Invalid request parameters` 拒绝。共用 CodeBuddy 代理现为流式与聚合响应生成按调用索引稳定的非空 ID，同时延续增量函数名；转发历史前会修复可无歧义配对的空 `tool_calls[].id` / `tool_call_id`，歧义则本地明确拒绝。该行为覆盖全部 CodeBuddy Profile 模型。2026-09-02 已安装 `0.9.3 (build 12)`；真实 `hy4-preview` 工具调用及工具结果第二轮均返回 HTTP 200，歧义历史在本地明确返回 400。最新 WorkBuddy CLI 目录当前只允许 `hy4-preview` 与 `hy3`，不再允许 `hy4-preview-x`；后者定义仍保留，但代理不会在目录重新允许前枚举或转发。
- **已授权清理**：2026-08-30 经用户明确授权，已删除仓库外 CodeBuddy 隔离验证状态 `~/.grok/build-state/grok-build-switch-codebuddy-subset-20260830`（约 14 MB）和安装前配置快照 `~/.grok/build-state/grok-build-switch-preinstall-config-20260830T205215`（约 48 KB）。删除前确认目录未被进程打开；当前安装、运行配置和订阅凭据未删除。
- **Grok Build 推理强度兼容边界**：2026-08-30 经本机 Grok Build `1.0.13` 实测，`max` 是可用的最高原生推理档位，`ultra` 会使该模型整组自定义档位失效。当前源码与 UI 暂只接入到 `max`：可信 Codex Standard/Fast 均声明到 `max`，`ultra` 被服务端拒绝且不投影到 `config.toml`；Switch 内部的 `reasoning_efforts_source` 只留在 Profile/路由状态，不再写入 Grok 的未知字段。
- **历史 GitHub 基线**：2026-08-30 曾将 CodeBuddy 本机目录同步、当时的 Codex Sol Ultra 实现、托管 Profile/路由边界加固、测试和文档推送到 `https://github.com/CyberStaZJU/grok-build-switch` 的 `main`。历史功能提交 `ef5d4522e75e322f08cc9a0e12cd217526050234` 已由本地 `HEAD`、`origin/main` 与远端 advertised ref 核验一致；提交不包含构建产物、运行数据、凭据或私有 home 路径。后续 max-only 改动已取代该提交中的 Ultra 运行行为。
- **历史发布前边界加固（已被 max-only 决策取代 Ultra 部分）**：2026-08-30 GitHub 同步前独立审查并修复四类边界：路由按具体模型的可信声明校验推理强度，当时 Terra/Luna Standard/Fast 以及“自定义显示名 + 可信模型 ID”不能绕过 `ultra` 限制，Sol Standard/Fast 保持支持；供应商编辑页保留已保存且由模型声明的 `low` 等非全局菜单值；CodeBuddy 本机目录跨文件按嵌入时间戳选择最新有效完整快照；CodeBuddy Profile 在后续路由或激活失败时恢复原 Profile 或删除本次新建项。隔离源码实例已通过桌面 context-only 保存 `low`、`390×844` 移动布局及持久化验证；全量 Go、vet、race、前端 Node 与 Wails-tag 测试通过。当前运行行为以本节的 max-only 条目为准。
- **历史本机安装（已由0.9.5 build16取代）**：2026-09-02 已构建并安装 `0.9.3 (build 12)` 到 `/Applications/Grok Build Switch.app`，主程序 SHA-256 为 `b151d543a47347c0a2c8a0fb35d3260e52bc7a57906bf528ccf4a3e432c96ce6`，与 `~/.grok/build-state/grok-build-switch-codebuddy-tool-fix-20260902/macos/Grok Build Switch.app` 逐字节一致。严格 codesign 校验通过，但仍为 ad-hoc 签名，没有 Developer ID 签名或 Apple 公证。PID 64010 从安装路径运行并监听 `127.0.0.1:17878`；健康接口、config/routing 一致性以及真实桌面和 `390×844` 移动页面通过。首次仓库内构建因 FinderInfo 签名失败且构建脚本先清空 `dist/macos`，原 0.9.2 DMG 未找到副本可恢复；失败生成的部分 0.9.3 App 已删除，当前仓库 `dist/macos` 为空。经校验的 0.9.3 App、DMG 和 `.sha256` 保留在上述仓库外 build-state。
- **Codex 推理强度最高接入到 Max**：`gpt-5.6-sol` 的 Standard 与 Fast 逻辑路由均显式声明 `low / medium / high / xhigh / max`，不再声明 `ultra`。供应商编辑页只提供 `medium / high / xhigh / max / none`；服务端会过滤陈旧 `ultra` 元数据并拒绝将其作为默认值，Switch 内部 `reasoning_efforts_source` 不再投影到 Grok `config.toml`。本机 Grok Build `1.0.13` 已实测 Standard/Fast 的 `max` 均返回 `OK`，`ultra` 被明确拒绝。真实托管 Profile 编辑页提交 `max` 成功，证明 effort-only 保存不再误报 409；验证后 Profile 恢复 Standard+Medium，全局路由恢复 Fast+High，`config.toml`、routing 和运行 API 一致。
- **官方 Grok CLI 登录与路由**：沿用 Grok CLI 官方登录流程，登录后可切换官方模型路由。
- **普通 Profile**：管理供应商、Base URL、API Key、上游格式与常用模型。模型卡片支持显式「上下文窗口」；已知模型（Kimi k3-256k、Codex gpt-5.6-*、Gemini gemini-3.7-flash-high、订阅 grok-4.5/4.6、CodeBuddy hy3/deepseek-v4-flash/deepseek-v4-pro）在 UI 预填并在服务端按 `profiles.KnownContextWindow` 兜底填入建议值，0 仍表示省略、由 Grok 用自身默认。Codex gpt-5.6-sol 与 gpt-6-astra 订阅代理模型的建议窗口为 320,000 tokens；订阅代理 Profile 允许在供应商编辑页调整上下文窗口和默认推理强度，其他托管字段仍通过订阅代理页面更新；编辑表单会保留上游已声明但不在当前菜单中的推理档位（如 Codex 的 `low`），避免保存上下文窗口时丢失模型元数据。CodeBuddy 的 DeepSeek v4 Flash/Pro 使用 1,000,000 的配置窗口，为已验证的 1,048,576 网关上限保留系统提示、工具调用和输出余量。

- **辅助模型键（2026-09-18）**：启用自定义供应商时，除 `[models].default` 外还写入 `[models].session_summary = <供应商默认模型>`；切回官方账号清除该键，换供应商改指新供应商默认模型。原因是 Grok Build 的会话标题/摘要走这个键，未设置时请求内置 Grok 模型，第三方网关返回 404。当前只接管该键：`image_description`、`prompt_suggestion`、`web_search` 经实测不会在此场景退回内置模型。

- **上游连接测试（2026-09-18）**：`probeModel` 不再把 HTML 200 当成功；网关对未实现的协议路径返回首页 HTML 时会判失败并提示换协议或补 `/v1`。只拒绝 HTML，不影响 JSON 网关、`messages` 兼容网关与 204 空响应的既有路径。
- **供应商默认模型写入路由**：在供应商编辑页设置 default 模型与推理强度；保存后事务性更新 `config.toml` 与 `routing.json`。explore / plan 跟随该 default（无独立「模型路由」页）。
- **托管 Profile 编辑页往返（2026-09-15）**：CodeBuddy 托管供应商此前在供应商编辑页保存必然 409，即使未做任何修改。原因是编辑表单的「默认推理强度」下拉没有空选项，未设置过档位的托管 Profile（存储值 `""`）回传时被规范成 `none`，而 `managedProfileEditableUpdate` 逐字比较托管默认值，把未修改的请求判为所有权变更。修复为把未设置与 `none` 视为同一值，并在等价时保留存储值；真实改动托管默认值仍返回 409，订阅代理的允许项（上下文窗口、已声明档位）与拒绝项（未声明档位）行为不变。普通 Profile 保存路径不受影响。
- **模型级推理强度声明（2026-09-15）**：模型卡片「模型高级设置」新增「支持推理强度」开关与 low / medium / high / xhigh / max 档位勾选。勾选后 Switch 写入 `supports_reasoning_effort = true` 与 `reasoning_efforts = [...]`（内部来源标记为 `declared`）；未勾选即不声明，不伪造能力。Grok Build 只为此处声明过的模型提供档位选择，因此普通供应商（不再局限于可信 Codex 订阅与 CodeBuddy 目录）也能在 Grok `/m`、`/effort` 中选择推理强度。默认模型声明的档位会收窄供应商级「默认推理强度」菜单；档位列表为空时不再保留 `declared` 来源，避免 Grok 拒绝默认档位。已按此声明 DeepSeek 官方 `https://api.deepseek.com/` 的 `deepseek-flash`：`low / medium / high / xhigh / max`，默认 `high`；`https://api.deepseek.com/chat/completions` 会对 `reasoning_effort` 做枚举校验（`none/minimal/low/medium/high/xhigh/max`），声明外取值被拒绝。验证：Grok Build `1.0.30` ACP `session/new` 中 `deepseek-flash` 由无 `supportsReasoningEffort` 变为 `supportsReasoningEffort: true`、`reasoningEffort: high`、五档 `reasoningEfforts`；真实 headless 请求 `grok -p ... -m deepseek-flash --reasoning-effort low` 返回 `pong`，未被丢弃或告警；`low`/`max` 直连 DeepSeek 官方均 HTTP 200。
- **用量观察**：聚合 prompt、cached prompt、completion、reasoning token 与缓存命中率，不展示 transcript、不推算美元成本。
- **订阅代理**：内嵌 CLIProxyAPI，负责受支持订阅账号的接入、状态和代理路由。
- **CodeBuddy / WorkBuddy 模型接入**：Switch 以本机 loopback OpenAI Chat Completions 代理直接请求固定推理端点 `https://copilot.tencent.com/v2/chat/completions`，不使用 WorkBuddy agent harness。`/v2/chat/completions` 本身不提供模型枚举；CodeBuddy 页面通过 `GET /api/codebuddy/models` 和“刷新模型目录”只读解析 WorkBuddy 本机已同步的产品目录，只采用 `cli` agent 明确允许、支持 tool call 且模型 ID 合法的条目，并显示来源和更新时间。目录缺失或损坏时使用 Switch 内置已验证兜底；普通启动不扩张用户模型子集。2026-08-30 页面新增 CodeBuddy 专属“暴露给 Grok Build”多选：默认模型必须属于所选子集；保存只更新 CodeBuddy Profile 并重新投影组合路由，其他供应商模型保持不变。隔离浏览器实例已验证只选 `hy4-preview` 后 CodeBuddy Profile、`config.toml` 与代理 `/v1/models` 均只保留该模型，另一个供应商的模型仍存在；空子集被前后端拒绝，桌面流程和 `390×844` 移动布局无横向溢出。2026-08-28 本机目录包含 16 个 CLI 模型，包括 `kimi-k3-2`、`hy4-preview`、`glm-5.3`、`glm-5.3-flash`；四者均已用现有 CodeBuddy API Key 最小直连验证 HTTP 200。CodeBuddy 托管 Profile 的普通编辑入口仅允许调整上下文窗口，目录、密钥、暴露子集和默认模型必须走 CodeBuddy 页面。当天已安装 `0.9.0 (build 9)` ad-hoc 本地候选并在真实实例同步该目录；CodeBuddy 保持未激活，默认供应商仍为 Codex Sol，四个新模型经页面逐一连通返回 `pong`。旧 0.8.0 安装包已保存在仓库外 build-state 供回滚；新包未经 Developer ID 签名或 Apple 公证。
- **配置编辑**：查看、校验和编辑 Grok CLI 的 `~/.grok/config.toml`。
- **菜单栏与桌面壳**：macOS 菜单栏、Wails 窗口、单实例与自动启动流程可用。
- **LAN 与 SSH**：局域网配对、CSRF 防护、SSH 连接和远程文件管理可用。

### 1.2 已收敛的产品边界

第 1.1 节是当前产品的完整功能范围。其他旧扩展已移除，不应以入口、兼容模式、接口清单、数据目录或待恢复功能继续出现在当前产品说明中。

### 1.3 旧 DataDir 记录清理授权

用户已明确授权清理上述已移除能力遗留在应用 DataDir 中的旧记录。清理范围必须严格限定为已移除能力的应用自有记录，并遵守以下边界：

- 不删除、不重置或改写 Grok CLI 官方认证；
- 不删除订阅代理保存的账号、令牌或其他凭据；
- 不把普通 Profile、统一路由、LAN、SSH 或应用设置误判为遗留记录；
- 对无法确认归属的文件先保留并报告，不扩大清理范围。

2026-07-31 已删除应用 DataDir 中旧的 `backups/` 目录（10 个 TOML 文件）；官方认证、订阅代理数据、普通 Profile、统一路由、应用设置、SSH 与主日志均已核验保留。其他能够确认属于已移除能力的历史记录仍按上述边界单独审计，不在此处声明已全部清理。

---

## 2. 当前行为边界

### 2.1 单一启用供应商与路由切换

`routing.json` 当前使用 schema v2：保存唯一 `active_provider_id`，并为每个供应商分别记忆 default、web_search、explore、plan 与默认推理强度。自定义供应商默认全部进入混合路由目录，不必再逐个启用。官方账号仍是互斥特例，但不与自定义认证混用。产品 UI 已去掉独立「模型路由」页：保存供应商时把其默认模型写入 Grok `default`，并强制 explore / plan 跟随；Switch 可保存 medium / high / xhigh / max / none。`max` 是推理强度元数据，不是独立模型，所以不会作为模型行出现在 `/m`；本机 Grok Build `1.0.13` 已实测 `/effort max` 和 `--effort max` 可用。`ultra` 暂不接入：服务端拒绝该默认值，模型能力和 `config.toml` 投影中均不包含它。

- v1 按 default 路由所属供应商确定启用项；跨供应商的 web_search、explore 与 plan 会分别迁移到其路由所属供应商的策略记忆。
- `config.toml` 保留全部自定义模型定义；web_search 仍由后端在具备能力时选用或修复。
- `/m` 混合显示各供应商已启用的模型（例如 CodeBuddy 只贡献 hy3/flash，反代仍可见）；官方 default 时清除自定义 `[model.*]`（档案仍在）。
- 可删除任一自定义供应商（含当前 default 所属）；也可删除官方登录（清除 `auth.json` 并回落到自定义 default）。
- 首页「设为默认」只改 default / effort，不收缩其他供应商的已启用模型。

路由修改执行以下事务：

1. 合并并严格校验请求字段；
2. 投影完整路由并预览目标 TOML；
3. 原子更新 `~/.grok/config.toml`；
4. 持久化 `routing.json`；
5. 若持久化失败，回滚本次配置写入。

路由切换只处理配置与模型选择。

### 2.2 Standard/Fast 路由与 Max Collaboration

订阅代理对 exact registry 中 `gpt-5.6-terra`、`gpt-5.6-sol`、`gpt-5.6-luna` 生成 Standard/Fast 逻辑路由对。Standard 保留 `subscription/codex/<physical-id>`；Fast 使用 `subscription/codex/<physical-id>-fast`，仍映射同一物理模型。CLIProxy 通过 canonical `config-ownership.json`、带内容指纹的 YAML marker 和完整 YAML merge，只对三条 exact Fast alias 注入 `service_tier: priority`；Standard 不注入。显式 reconciliation 在目录发现前取得进程内与 DataDir 跨进程锁，执行 GET → merge → 二次 GET/rebase → write-ahead journal → PUT → 语义/ledger 验证，并等待新增受管别名出现、已移除受管别名消失且 raw catalog 连续两次稳定；普通 `Models` 状态查询保持只读。管理 API 无 ETag/CAS，仍无法让不合作的外部写入者获得真正原子性；未知 post-PUT 语义状态会 fail closed、保留 recovery journal 且不推进 ownership ledger。


- routing 仍保持 schema v2；Collaboration Policy 使用独立 schema v5，保存在应用 DataDir 的 `collaboration.json`。每个角色保存 Standard route anchor、`speed_tier` 与 `reasoning_effort`。旧 schema v1/v2/v3/v4 均严格解码并只在内存迁移为 v5；v1 映射三角色与全局 effort，v2 保留四角色 model/effort 并固定 Standard，v3 保留四角色 anchor/speed/effort；三者都复制顶层 provider 到各角色、写入 workflow 派生的固定 data scope、保持 federation consent 为空。读取不重写旧文件，下一次显式保存才持久化 v5。
- v1 的 coordinator → 主协调与主实现、evidence → 任务拆解、builder → 困难实现 / 复核，旧全局 effort 复制到四角色；v2 保留四角色模型/effort。两者都不会根据 `-fast` 后缀自动提高速度档：旧具体 Fast ID 若不能作为可信 Standard 锚点解析会 fail closed，等待用户显式修复。
- Standard 使用现有逻辑身份且不注入 priority；Fast 仅解析到 exact-registry 可信 Terra/Sol/Luna partner，由 CLIProxy 对精确 `-fast` 别名注入 `service_tier: priority`。缺失、歧义或伪造关系不回退。速度与 effort 相互独立；Fast 通常更快但消耗更多订阅 credits，无固定倍率声明。所有可信 Codex Standard/Fast 路由的推理档位当前统一到 `max`，不声明 `ultra`。
- 能力校验 fail closed：四个锚点必须属于当前启用的同一可信 Codex 订阅供应商；每个解析后的具体 Standard/Fast route 都必须 `supports_reasoning_effort=true`、来源为 `declared` 或 `probe`、支持列表明确包含该角色所选 effort。
- preview 无副作用；apply 需要用户确认和最新 fingerprint；端点全部 loopback-only、strict JSON、CSRF 保护。
- enabled apply 写入顺序为 artifacts → config → routing → policy；后段失败会补偿回滚。写入前再次核对文件状态，避免 stale preview 覆盖并发用户编辑。
- 受管 manifest 必须恰好覆盖四个 agent definition、四个 role 和一个 workflow 的九个 canonical Grok Home 路径；agent definition 才会注册 workflow 可用的自定义 `agent_type`，role TOML 仅提供解析覆盖。三个旧 basename 保持稳定以延续已有文件所有权。除升级所需的精确五文件 legacy manifest 外，非 canonical/部分/空 manifest、未受管同名文件、缺失/hash 漂移、符号链接或非普通文件都会 fail closed；下一次 enabled apply 会事务升级为九文件 manifest。
- apply 只把普通路由的 `default` **具体 Standard/Fast route**与默认推理强度对齐到主协调；`web_search`、`explore` 和 `plan` 保持原选择，浏览器/搜索能力与 Collaboration 正交。
- disable 是 policy-only：只切换 enabled 状态并保留 provider、四角色锚点/速度档/effort、默认 tier、manifest 与磁盘 agent/role/workflow，不改写 config/routing；即使 routing 无法读取也可停用。状态读取仍检查保留 artifact 的 manifest、文件类型与 hash 漂移。
- 生成 workflow 顶层阶段串行且精确预算 fail closed；默认 128 budget 被拒绝。Economy 只调用主协调；Focused Evidence 调用任务拆解 → 主协调；Focused Build、Assurance、Critical 的由 workflow 顶层启动 10 个主实现 agent，再分别进入主协调或困难实现 / 复核；不使用 workflow `resume_from`。named slash launch 当前不能携带精确 budget，必须使用 UI 生成的复制式自然语言指令，让 Grok 调用 workflow tool。
- UI 只列 Standard 锚点，并为每角色独立解析速度档；已保存但当前缺失的锚点、消失的 Fast partner，或不再受支持 / 不再具备可信 capability 来源的 effort，都会作为禁用的已选项保留，直到用户显式替换；空速度值不会被制造成 Standard，停用后四角色选择也继续显示。
- Switch 不启动 agent，不保存消息、transcript 或 session graph；Grok Build 是唯一执行面。

生产 renderer 的五条 tier 均有路径/预算/角色组合单元覆盖。2026-08-04 使用生产 renderer 导出的脚本执行 top-level `validate_only` 时，脚本可以编译，但 canned host 未采用请求提供的精确 budget，触发生产脚本的预算 fail-closed 门槛；因此不能把五条路径记为 path-specific PASS。另以精确 `agent_budget=1` 完成一次 Economy 最小只读 live smoke：只调用主协调，没有额外 child、文件修改或外部动作。该 smoke 不证明其他 tier、Fast priority、真实订阅成本、质量或节省比例。

当前源码中的 Max Collaboration 卡片提供复制式启动区：根据 tier 显示 1/2/11/12/13 精确 budget，要求填写 objective，并生成可粘贴到 Grok Build 的自然语言指令。直接 `/gbs-max-collab` 仍会使用 named workflow 默认 budget 128，且不会弹出参数选择器，因此源码、文档和新生成 workflow 的 metadata 都明确要求由 Grok 通过 workflow tool 启动。federation disclosure 也已改为独立信息卡、精确 edge map、传递边界说明与明确同意复选框。升级前已经写入 `~/.grok` 的旧 workflow 不会被后台静默覆盖；用户需在新版本中再次预览并应用 Collaboration，才会生成带最新 metadata 的 artifact。

订阅代理保存流程现在会优先识别当前 server-owned Profile；升级旧版本时，只会接管名称、Base URL 和全部模型 alias 都精确匹配且唯一的未标记 legacy Profile，多个候选则 fail closed。2026-08-04 已按用户授权删除一个不活动的重复旧 Codex subscription Profile，保留唯一活动供应商，并补充防复发测试。

2026-08-04 已完成隔离的本地发布候选验证：默认/Wails 测试与构建、关键包 race、`go vet`、前端 Node 测试均通过；外部 build-state 中的全新 arm64 `.app`/DMG 通过 ad-hoc 签名、bundle 内容、macOS 15.0 minimum target、DMG SHA-256 与隔离 HOME/DataDir/Grok Home 的四个只读核心端点 smoke。2026-08-19 的 CodeBuddy 修复发布前再次通过前端 Node 测试、`go test ./...`、`go vet ./...`、`go test -race ./internal/codebuddy ./internal/profiles ./internal/server` 与 `go test -tags wailsgui .`；待提交代码和轻量文档的常见密钥格式及私有 home 路径扫描无发现。最新**全量** race 与独立 `check-work` 仍是正式安装包发布前待办。Developer ID 签名和公证当前被本机缺少有效 `Developer ID Application` 身份及可用 `notarytool` profile 阻塞；不得把 ad-hoc 签名描述为正式签名或公证。

### 2.3 安全与上游边界

- 已配对 LAN 客户端仅能读取脱敏 Profile 元数据；原始配置、SSH、凭据探测和管理写操作仅允许 loopback。
- 路由与状态 GET 只返回修复建议，不再持久化切换；修复必须通过显式修改请求执行。
- Profile ID 始终由服务端生成，重复或空 ID 的持久化数据会被隔离并停止参与路由。
- 不提供 Anthropic 官方 API 直连或类型模板；`messages` 只保留给明确支持该协议的第三方兼容网关。Profile 的协议格式由显式选择的 `upstream_format` 决定。
- 推理强度探测必须由用户明确确认，最多发送 6 个最小上游请求，`none` 仅作为本地禁用哨兵而不发送。

### 2.4 web_search 能力

`WebSearchCapable` 只表示所选路由的原生搜索能力。自定义供应商的非空 web_search 必须同时使用 `responses` 后端并声明 `SupportsBackendSearch`；前端只列出符合条件的路由，服务端仍执行权威校验并在失败时保持配置与路由不变。应用不为不兼容路由声明额外浏览器回退；用户应选择支持的路由或在 Grok CLI 自身配置所需 MCP。

### 2.5 用户数据

权威代码位于 Git 仓库；运行配置、凭据和本机状态位于应用 DataDir 或 Grok CLI 自身目录。`collaboration.json` 以及 Switch 生成的用户级 agent/role/workflow 都属于当前保留能力。官方认证与订阅代理凭据均属于保留数据，不在本轮旧记录清理范围内。

---

## 3. 已知问题与技术债

- `server.go` 仍较大，可继续按配置、模型探测等资源拆分。
- 直接编辑 TOML 的入口仍需要持续加强与统一路由策略的一致性校验。
- `SupportsBackendSearch` 与模型级推理强度声明都主要依赖 Profile 声明，尚未实现通用自动探测。
- `profileMutationModelDTO` 不携带 `stream_tool_calls`：普通 Profile 的模型若带该字段，经供应商编辑页保存会丢字段（托管 Profile 因走 `previous` 重建路径不受影响）。仅代码路径确认，尚未端到端复现。
- CodeBuddy 最新模型目录依赖 WorkBuddy 已在本机同步产品配置；Switch 不调用未确认的远程枚举端点，也不会从 `/v2/chat/completions` 猜测目录。没有本机目录时会明确回退到内置兜底，因此“最新”取决于 WorkBuddy 本地缓存的新鲜度。
- 用量日志的 per-turn 事件不直接携带模型 ID；当前按 session `summary.json` 的模型归属，模型中途切换的历史 turn 可能被归到当前模型。UI 已明确标注这是近似归属，不应作为精确 per-model 计费证据。
- 仅完成一次经授权的 Economy 最小只读 live smoke；Focused Evidence、Focused Build、Assurance、Critical、Fast priority 与跨角色 handoff 仍未做真实试点。真实多任务成本/质量/返工率对照已从当前版本完成门槛中移除；除非未来另立研究项目并获得足够证据，否则不宣称实际节省比例或质量优势。
- 当前不支持项目级 Collaboration artifact export，也不提供已生成文件的自动删除；停用只保留文件并继续观测其漂移。
- CLIProxy 完整 YAML 合并已有跨进程 operation lock、write-ahead recovery journal、post-PUT 语义/ledger 核验和 raw catalog 稳定收敛；但上游管理端点没有 ETag/CAS，无法与不遵守 Switch lock 的外部写入者形成真正原子事务。未知语义状态会保留 journal 并 fail closed，需要先恢复/人工核对，不能声称已应用。
- 多个 `grok` 会话共用同一份 `~/.grok/config.toml`，各自 `/model` 选择会互相覆盖 `[models].default`；Switch 侧写入不具排他性。`config_matches_active=false` 因此既可能来自并发写入，也可能是不一致，需结合 `[model.*]` 路由表判断。
- 供应商 `available_models` 是拉取时的快照，不随上游 `/v1/models` 实时校验：上游下线某模型后 Switch 仍会列出并在 `/model` 菜单中提供（API 池的 `gpt-6-astra` 即实例），选中后得到上游 404。
- 供应商编辑页保存只回传表单字段：凡 Profile 上「不在表单里出现」的字段都依赖服务端显式保留（如流式保护的 `upstream_base_url`）。新增此类字段时必须同时补一条**经 handler 驱动**的往返测试——直接调 `store.Update` 的测试绕过 handler，测不到该路径。
- DataDir 清理需要按明确归属执行；未知记录不得自动删除。
- 能力探测的缓存没有失效策略：模型一旦记为 `efforts_known` 就不再重测，因此上游后来改变档位或新增 Fast 支持不会被发现；需要时只能删除 `cliproxy/capability.json` 重新探测。
- Fast 缺少可验证的成功信号（`service_tier` 不被校验且响应恒为 `default`），当前以「代理接受该模型」为准开启；这与 docs/agent.md 中「不得把无法验证的能力表述为已验证」的边界一致，但需要在 UI/文档措辞上持续保持诚实。
- `cliproxy/config.yaml` 的 2 条重复 priority 规则为先于 0.9.15 的既有漂移，尚未清理。
- 正式 macOS 发布仍需本机可用的 Developer ID Application 证书与 notarytool 凭据；缺失时只能生成 ad-hoc 本地候选，不能发布为已签名/已公证资产。

---

## 4. 环境与观测

| 项目 | 状态 |
|:---|:---|
| Go | 由 `go.mod` 锁定 |
| macOS | 15+，Apple Silicon / arm64 |
| CLIProxyAPI | 内嵌固定版本，由构建脚本校验 |
| Grok CLI | 用于官方登录、配置和模型执行 |

- 应用日志位于 DataDir。
- CLIProxyAPI 日志位于其 DataDir 子目录。
- 健康检查使用 `GET /api/status`。
- Collaboration 状态使用 loopback-only `GET /api/collaboration`；preview/apply 分别使用 `POST /api/collaboration/preview` 与 `PUT /api/collaboration`。
- 用量面板读取 `~/.grok/logs/unified.jsonl`，不读取消息正文。
- 默认禁用远程遥测。


> Collaboration schema v5 defaults to `single_provider`. `federated` is an explicit-consent preview model with per-role provider and data-scope assignments; current active-provider/config serialization blocks safe multi-provider activation, so the Switch fails closed rather than merging credentials or pretending cross-provider routing works.
