# Grok Build Switch — Status 文档

> 当前状态、产品边界与技术债。最后更新：2026-09-05（Grok专用0.9.4 build18；保留GPT订阅代理）。

---

## 当前产品方向与回退（2026-09-05）

按用户决定撤回 Codex 客户端适配，仅管理 Grok；保留 GPT/ChatGPT 订阅代理、现有供应商与 WorkBuddy Chat 模型。已安装 0.9.4 build18 Grok 专用维护包（ad-hoc，未公证）：相对旧 build15 保留模型子集不扩张、无变化不重写 Profile、移动布局和空 SSH 列表修复。Codex 客户端 UI/API、配置写入模块与 WorkBuddy Responses 桥接均已从当前源码移除；CLIProxyAPI 的 GPT Responses 推理路由保留。

安装后通过界面保存事务恢复原 hy4-preview 单模型选择。Grok config、Codex config/auth、设置与回退前字节一致；GPT/订阅 Profile 未变，WorkBuddy Profile 和 routing 仅 updated_at 变化。原 CLIProxy 进程保留。没有发起新的真实模型付费请求；GPT 反代保留结论基于代码/配置、路由与页面检查，不冒充本轮真实推理验收。

回退前源码、App和私有快照位于 `~/.grok/build-state/grok-build-switch-grok-rollback-20260905/`；最终构建位于 `/private/tmp/gbs-grok-rollback-20260905/`。已有 Codex 实验资料及旧 App 未删除，源代码未提交/推送。上次真实 Codex 请求曾返回11128，具体触发字段未定位；该方向已取消，不列为继续调试任务。用户级个人规则及其他工具配置不属于此次撤回范围。

验证：全量Go、vet、关键代理/server race、前端22项、构建签名与安装校验通过。生产界面已实际恢复模型选择并访问GPT订阅代理；1280桌面及390×844移动端首页、WorkBuddy、订阅代理、设置、SSH真实导航通过，无横向溢出。无需额外删除批准即可使用当前版本；外部备份留存，清理另行决定。

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
- **普通 Profile**：管理供应商、Base URL、API Key、上游格式与常用模型。模型卡片支持显式「上下文窗口」；已知模型（Kimi k3-256k、Codex gpt-5.6-*、Gemini gemini-3.7-flash-high、订阅 grok-4.5/4.6、CodeBuddy hy3/deepseek-v4-flash/deepseek-v4-pro）在 UI 预填并在服务端按 `profiles.KnownContextWindow` 兜底填入建议值，0 仍表示省略、由 Grok 用自身默认。Codex GPT-5.6 订阅代理模型的建议窗口为 320,000 tokens；订阅代理 Profile 允许在供应商编辑页调整上下文窗口和默认推理强度，其他托管字段仍通过订阅代理页面更新；编辑表单会保留上游已声明但不在当前菜单中的推理档位（如 Codex 的 `low`），避免保存上下文窗口时丢失模型元数据。CodeBuddy 的 DeepSeek v4 Flash/Pro 使用 1,000,000 的配置窗口，为已验证的 1,048,576 网关上限保留系统提示、工具调用和输出余量。
- **供应商默认模型写入路由**：在供应商编辑页设置 default 模型与推理强度；保存后事务性更新 `config.toml` 与 `routing.json`。explore / plan 跟随该 default（无独立「模型路由」页）。
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
- `SupportsBackendSearch` 主要依赖 Profile 声明，尚未实现通用自动探测。
- CodeBuddy 最新模型目录依赖 WorkBuddy 已在本机同步产品配置；Switch 不调用未确认的远程枚举端点，也不会从 `/v2/chat/completions` 猜测目录。没有本机目录时会明确回退到内置兜底，因此“最新”取决于 WorkBuddy 本地缓存的新鲜度。
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


> Collaboration schema v5 defaults to `single_provider`. `federated` is an explicit-consent preview model with per-role provider and data-scope assignments; current active-provider/config serialization blocks safe multi-provider activation, so the Switch fails closed rather than merging credentials or pretending cross-provider routing works.
