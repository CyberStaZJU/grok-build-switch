# Grok Build Switch — Status 文档

> 当前状态、产品边界与技术债。最后更新：2026-08-04。

---

## 1. 当前状态

### 1.1 保留并维护的能力

- **官方 Grok CLI 登录与路由**：沿用 Grok CLI 官方登录流程，登录后可切换官方模型路由。
- **普通 Profile**：管理供应商、Base URL、API Key、上游格式与常用模型。
- **统一模型路由**：事务性更新 `config.toml` 和 `routing.json`，覆盖 default、web_search、explore 与 plan。
- **Max Collaboration 控制面**：独立 schema v4 policy；为主协调、任务拆解、主实现、困难实现 / 复核四个角色分别验证同一可信 Codex 订阅供应商内的 Standard 锚点、Standard/Fast 速度档与推理强度，生成用户级 agent definition、role 与 workflow，并以 Economy=1、Focused=2、Assurance=3、Critical=4 的串行精确预算门控 Grok Build。
- **用量观察**：聚合 prompt、cached prompt、completion、reasoning token 与缓存命中率，不展示 transcript、不推算美元成本。
- **订阅代理**：内嵌 CLIProxyAPI，负责受支持订阅账号的接入、状态和代理路由。
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

`routing.json` 当前使用 schema v2：保存唯一 `active_provider_id`，并为每个供应商分别记忆 default、web_search、explore、plan 与默认推理强度。存在供应商时不能全部关闭；官方账号作为特殊供应商参与互斥启用，但不与自定义认证混用。

- v1 按 default 路由所属供应商确定启用项；跨供应商的 web_search、explore 与 plan 会分别迁移到其路由所属供应商的策略记忆，不改变启用项。
- 自定义供应商之间切换时，`config.toml` 保留全部自定义模型定义，旧会话固定的旧别名仍可解析；四类当前路由只能来自启用供应商。
- 切换官方账号会移除自定义模型定义与认证；浏览器界面在执行前明确提示兼容性影响。
- 每个供应商再次启用时恢复自己上次保存的路由策略。
- 启用供应商不能直接删除，必须先启用另一个供应商。

路由修改执行以下事务：

1. 合并并严格校验请求字段；
2. 投影完整路由并预览目标 TOML；
3. 原子更新 `~/.grok/config.toml`；
4. 持久化 `routing.json`；
5. 若持久化失败，回滚本次配置写入。

路由切换只处理配置与模型选择。

### 2.2 Standard/Fast 路由与 Max Collaboration

订阅代理对 exact registry 中 `gpt-5.6-terra`、`gpt-5.6-sol`、`gpt-5.6-luna` 生成 Standard/Fast 逻辑路由对。Standard 保留 `subscription/codex/<physical-id>`；Fast 使用 `subscription/codex/<physical-id>-fast`，仍映射同一物理模型。CLIProxy 通过 canonical `config-ownership.json`、带内容指纹的 YAML marker 和完整 YAML merge，只对三条 exact Fast alias 注入 `service_tier: priority`；Standard 不注入。显式 reconciliation 在目录发现前取得进程内与 DataDir 跨进程锁，执行 GET → merge → 二次 GET/rebase → write-ahead journal → PUT → 语义/ledger 验证，并等待新增受管别名出现、已移除受管别名消失且 raw catalog 连续两次稳定；普通 `Models` 状态查询保持只读。管理 API 无 ETag/CAS，仍无法让不合作的外部写入者获得真正原子性；未知 post-PUT 语义状态会 fail closed、保留 recovery journal 且不推进 ownership ledger。


- routing 仍保持 schema v2；Collaboration Policy 使用独立 schema v4，保存在应用 DataDir 的 `collaboration.json`。每个角色保存 Standard route anchor、`speed_tier` 与 `reasoning_effort`。旧 schema v1/v2/v3 均严格解码并只在内存迁移为 v4；v1 映射三角色与全局 effort，v2 保留四角色 model/effort 并固定 Standard，v3 保留四角色 anchor/speed/effort；三者都复制顶层 provider 到各角色、写入 workflow 派生的固定 data scope、保持 federation consent 为空。读取不重写旧文件，下一次显式保存才持久化 v4。
- v1 的 coordinator → 主协调与主实现、evidence → 任务拆解、builder → 困难实现 / 复核，旧全局 effort 复制到四角色；v2 保留四角色模型/effort。两者都不会根据 `-fast` 后缀自动提高速度档：旧具体 Fast ID 若不能作为可信 Standard 锚点解析会 fail closed，等待用户显式修复。
- Standard 使用现有逻辑身份且不注入 priority；Fast 仅解析到 exact-registry 可信 Terra/Sol/Luna partner，由 CLIProxy 对精确 `-fast` 别名注入 `service_tier: priority`。缺失、歧义或伪造关系不回退。速度与 effort 相互独立；Fast 通常更快但消耗更多订阅 credits，无固定倍率声明。
- 能力校验 fail closed：四个锚点必须属于当前启用的同一可信 Codex 订阅供应商；每个解析后的具体 Standard/Fast route 都必须 `supports_reasoning_effort=true`、来源为 `declared` 或 `probe`、支持列表明确包含该角色所选 effort。
- preview 无副作用；apply 需要用户确认和最新 fingerprint；端点全部 loopback-only、strict JSON、CSRF 保护。
- enabled apply 写入顺序为 artifacts → config → routing → policy；后段失败会补偿回滚。写入前再次核对文件状态，避免 stale preview 覆盖并发用户编辑。
- 受管 manifest 必须恰好覆盖四个 agent definition、四个 role 和一个 workflow 的九个 canonical Grok Home 路径；agent definition 才会注册 workflow 可用的自定义 `agent_type`，role TOML 仅提供解析覆盖。三个旧 basename 保持稳定以延续已有文件所有权。除升级所需的精确五文件 legacy manifest 外，非 canonical/部分/空 manifest、未受管同名文件、缺失/hash 漂移、符号链接或非普通文件都会 fail closed；下一次 enabled apply 会事务升级为九文件 manifest。
- apply 只把普通路由的 `default` **具体 Standard/Fast route**与默认推理强度对齐到主协调；`web_search`、`explore` 和 `plan` 保持原选择，浏览器/搜索能力与 Collaboration 正交。
- disable 是 policy-only：只切换 enabled 状态并保留 provider、四角色锚点/速度档/effort、默认 tier、manifest 与磁盘 agent/role/workflow，不改写 config/routing；即使 routing 无法读取也可停用。状态读取仍检查保留 artifact 的 manifest、文件类型与 hash 漂移。
- 生成 workflow 严格串行且精确预算 fail closed；默认 128 budget 被拒绝。Economy 只调用主协调；Focused Evidence 调用任务拆解 → 主协调；Focused Build 调用主实现 → 主协调；Assurance 调用任务拆解 → 主实现 → 主协调；Critical 调用任务拆解 → 主实现 → 困难实现 / 复核 → 主协调，不使用 `resume_from`。named slash launch 当前不能携带精确 budget，必须使用 UI 生成的复制式自然语言指令，让 Grok 调用 workflow tool。
- UI 只列 Standard 锚点，并为每角色独立解析速度档；已保存但当前缺失的锚点、消失的 Fast partner，或不再受支持 / 不再具备可信 capability 来源的 effort，都会作为禁用的已选项保留，直到用户显式替换；空速度值不会被制造成 Standard，停用后四角色选择也继续显示。
- Switch 不启动 agent，不保存消息、transcript 或 session graph；Grok Build 是唯一执行面。

生产 renderer 的五条 tier 均有路径/预算/角色组合单元覆盖。2026-08-04 使用生产 renderer 导出的脚本执行 top-level `validate_only` 时，脚本可以编译，但 canned host 未采用请求提供的精确 budget，触发生产脚本的预算 fail-closed 门槛；因此不能把五条路径记为 path-specific PASS。另以精确 `agent_budget=1` 完成一次 Economy 最小只读 live smoke：只调用主协调，没有额外 child、文件修改或外部动作。该 smoke 不证明其他 tier、Fast priority、真实订阅成本、质量或节省比例。

当前源码中的 Max Collaboration 卡片提供复制式启动区：根据 tier 显示 1/2/2/3/4 精确 budget，要求填写 objective，并生成可粘贴到 Grok Build 的自然语言指令。直接 `/gbs-max-collab` 仍会使用 named workflow 默认 budget 128，且不会弹出参数选择器，因此源码、文档和新生成 workflow 的 metadata 都明确要求由 Grok 通过 workflow tool 启动。federation disclosure 也已改为独立信息卡、精确 edge map、传递边界说明与明确同意复选框。升级前已经写入 `~/.grok` 的旧 workflow 不会被后台静默覆盖；用户需在新版本中再次预览并应用 Collaboration，才会生成带最新 metadata 的 artifact。

订阅代理保存流程现在会优先识别当前 server-owned Profile；升级旧版本时，只会接管名称、Base URL 和全部模型 alias 都精确匹配且唯一的未标记 legacy Profile，多个候选则 fail closed。2026-08-04 已按用户授权删除一个不活动的重复旧 Codex subscription Profile，保留唯一活动供应商，并补充防复发测试。

2026-08-04 已完成隔离的本地发布候选验证：默认/Wails 测试与构建、关键包 race、`go vet`、前端 Node 测试均通过；外部 build-state 中的全新 arm64 `.app`/DMG 通过 ad-hoc 签名、bundle 内容、macOS 15.0 minimum target、DMG SHA-256 与隔离 HOME/DataDir/Grok Home 的四个只读核心端点 smoke。最新全量 race 与独立 `check-work` 正在发布前重跑。Developer ID 签名和公证当前被本机缺少有效 `Developer ID Application` 身份及可用 `notarytool` profile 阻塞；不得把 ad-hoc 签名描述为正式签名或公证。

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
- `SupportsBackendSearch` 主要依赖 Profile 声明，尚未实现通用自动探测。
- 用量日志的 per-turn 事件不直接携带模型 ID；当前按 session `summary.json` 的模型归属，模型中途切换的历史 turn 可能被归到当前模型。UI 已明确标注这是近似归属，不应作为精确 per-model 计费证据。
- 仅完成一次经授权的 Economy 最小只读 live smoke；Focused Evidence、Focused Build、Assurance、Critical、Fast priority 与跨角色 handoff 仍未做真实试点。真实多任务成本/质量/返工率对照已从当前版本完成门槛中移除；除非未来另立研究项目并获得足够证据，否则不宣称实际节省比例或质量优势。
- 当前不支持项目级 Collaboration artifact export，也不提供已生成文件的自动删除；停用只保留文件并继续观测其漂移。
- CLIProxy 完整 YAML 合并已有跨进程 operation lock、write-ahead recovery journal、post-PUT 语义/ledger 核验和 raw catalog 稳定收敛；但上游管理端点没有 ETag/CAS，无法与不遵守 Switch lock 的外部写入者形成真正原子事务。未知语义状态会保留 journal 并 fail closed，需要先恢复/人工核对，不能声称已应用。
- DataDir 清理需要按明确归属执行；未知记录不得自动删除。
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


> Collaboration schema v4 defaults to `single_provider`. `federated` is an explicit-consent preview model with per-role provider and data-scope assignments; current active-provider/config serialization blocks safe multi-provider activation, so the Switch fails closed rather than merging credentials or pretending cross-provider routing works.
