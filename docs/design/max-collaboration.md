# Max Collaboration 控制面设计

> 状态：schema v4 四角色 Standard/Fast 控制面、复制式精确预算启动指引和本地单元/集成覆盖已实现；已完成一次 Economy 只读 live smoke，待最新全量 race、独立审查及其余 tier 的经授权试点。最后更新：2026-08-04。

## 1. 目标

Max Collaboration 让 Grok Build Switch 为 Grok Build 生成一个成本门控的四角色协作预设：

- **主协调**：范围控制、任务收敛、最终验证与交付；
- **任务拆解**：只读仓库探索，返回紧凑的约束、依赖、文件和测试工作包；
- **主实现**：完成有边界的实现和聚焦验证；
- **困难实现 / 复核**：按需处理高难实现、调试、对抗性复核和关键修复。

四个角色分别选择当前可信 Codex 订阅供应商中的 **Standard route anchor**、独立 `speed_tier`（`standard` / `fast`）与显式 `reasoning_effort`；多个角色可以复用同一锚点。速度与 effort 相互独立。优化目标是用明确的角色边界和精确串行预算减少不必要调用、并发、重复扫描、重试和返工，而不是把角色绑定到固定模型名、固定速度或固定推理档位。

## 2. 控制面与执行面

```text
Grok Build Switch                         Grok Build
-----------------                         ----------
选择 provider / 四角色 anchor + speed + effort  运行 agent()
解析可信 Standard/Fast 并校验具体 route effort    执行 workflow
预览 config / routing / role / workflow     消耗 agent_budget
原子应用和补偿回滚                          保存其自身 session/run 状态
展示 token/cache
```

Switch **不**启动 agent，不保存消息、transcript、agent ID 或 session graph，也不提供 `/api/agent/*`。Grok Build 是唯一 agent/workflow 执行面。当前生成 workflow 不使用 `resume_from`。

浏览器和 web search 能力与 Collaboration 正交：普通 routing 继续拥有 `web_search`、`explore` 和 `plan`；Collaboration 不用四个语义角色覆盖这些通用槽位。

## 3. 数据模型与迁移

Collaboration Policy 使用独立 schema v4，保持 routing schema v2 不变。文件位于应用 DataDir 的 `collaboration.json`，包含：

- 明确模式 `single_provider|federated`（默认及 v1/v2/v3 迁移均为 `single_provider`）；federated consent basis 固定为 `all_workflow_tiers_v1`，绑定精确 provider 集合、Economy/Focused Evidence/Focused Build/Assurance/Critical 五条可执行路径的逐跳跨 provider edge map、`bounded_work_products` 与 credentials/secrets/full_transcripts 永不传递常量；Adaptive 只是 prompt 默认提示，不是可执行路径，Default tier 改变不会缩小 consent；
- coordinator/primary provider ID，仅表示普通 routing default 的所有者；
- 主协调、任务拆解、主实现、困难实现 / 复核四个角色各自的显式 `provider_id`、稳定 Standard route anchor、`speed_tier`、推理强度与闭集 `data_scope`；
- 默认提示 tier；
- Economy=1、Focused=2、Assurance=3、Critical=4；
- `max_parallel = 1`、`retry_limit = 1`；
- 用户级 artifact scope；
- 受管文件的绝对路径与 SHA-256。

Policy 不保存凭据或 agent 运行数据。可选 provider 只来自普通 Profiles 生成的同一 routing snapshot，包括未激活的普通 Profile；不引入单独 credential source。对 UI 的 routing DTO 只暴露脱敏 provider/model/capability 元数据，不序列化 API Key、Base URL 或 headers。四角色 data scope 不是用户选项：任务拆解固定为 `repository_only`，其余角色固定为 `repository_plus_minimized_prior_work_products`，请求必须显式携带这些 canonical 值且服务端拒绝篡改。

schema v1/v2/v3 只作为严格迁移输入：

- v1 `coordinator` → 主协调和主实现；
- v1 `evidence` → 任务拆解；
- v1 `builder` → 困难实现 / 复核；
- v1 全局 `reasoning_effort` 复制到四个角色；
- v2 四角色 model/effort 原样进入对应 v4 assignment；
- v3 四角色 anchor/speed/effort 原样进入 v4；
- 旧顶层 provider 复制到每个角色，数据范围固定映射为任务拆解 `repository_only`、其他角色 `repository_plus_minimized_prior_work_products`；
- 两个旧版本的 `speed_tier` 均迁移为 `standard`。

v1、v2 和 v3 都拒绝未知字段、尾随数据和多份 JSON 文档。迁移结果必须完整通过 v4 校验；无效旧档位等畸形输入 fail closed。`Snapshot` 只在内存迁移，不重写原始旧字节；下一次显式 `Replace` 才持久化 v4。纯 store 迁移不会按 `-fast` 后缀推断关系：旧具体 Fast route ID 会以 Standard-tier model 字符串保留，随后若不能作为可信 Standard anchor 解析则 fail closed，要求用户显式修复。这一边界避免旧数据迁移时无意提高 subscription credit 档。disabled v1 保留 metadata 与 managed manifest，但不恢复活动角色选择。

## 4. Standard/Fast 解析与能力校验

启用预设时必须同时满足：

1. 当前启用的是 Switch 管理的 `subscription-proxy:codex` 可信供应商，不能是官方 provider 或任意自定义 provider；
2. 四个角色都选择 Standard anchor、`speed_tier` 和推理强度；角色之间允许选择同一 anchor；
3. Standard route 显式自锚定，且 `Model` / `ProfileModel` 精确等于 trusted registry 的 `subscription/codex/<physical-id>`；
4. `standard` 直接使用该 route，不注入 priority；
5. `fast` 必须在同一 provider 中恰好存在一个显式锚定该 Standard route 的 Fast partner，且其 `Model` / `ProfileModel` 精确等于 `<standard-alias>-fast`；缺失、歧义或伪造关系失败，绝不回退 Standard；
6. 当前 exact registry 只信任 `gpt-5.6-terra`、`gpt-5.6-sol`、`gpt-5.6-luna`，禁止从后缀、显示名、provider 名或 GPT 名称推断；
7. 对每个解析后的具体 Standard/Fast route，所选 effort 都满足：
   - `supports_reasoning_effort == true`；
   - `reasoning_efforts_source` 是 `declared` 或 `probe`；
   - `reasoning_efforts` 明确包含所选档位。

Fast 逻辑别名仍映射同一物理模型，CLIProxy 仅对三条 exact Fast alias 注入 `service_tier: priority`；Standard 不注入。Fast 通常降低延迟但会使用更多订阅 credits；控制面只给定性警告，不声称固定倍率。未知/default capability 或任何名称推测全部 fail closed。Reasoning-effort probe 会产生上游请求，只能沿用现有用户确认流程，不能由 Collaboration 自动触发。

### 4.1 CLIProxy payload 整形

Switch 不把 `service_tier` 写进 Grok `config.toml` 或 Collaboration role TOML。订阅代理层维护逻辑 Standard/Fast aliases 到同一物理模型的映射，并通过 `gopkg.in/yaml.v3` 对 CLIProxy 的**完整** YAML 做受控合并：GET 当前配置 → 合并 Switch ownership → 二次 GET/rebase → 写前 transaction journal → PUT → 语义验证 → 原子提交 ownership ledger。`config-ownership.json` 只接受 canonical channel，并精确记录 Switch 拥有的 alias/rule；YAML marker 使用内容指纹认证，CLIProxy 丢失注释时才允许 ledger + exact shape 的保守兼容。禁止用前缀、大小写折叠或未认证 marker 猜测所有权；旧未标记 alias 只在 ledger 缺失时经严格一次性迁移读取，不能夺取用户条目。

唯一受管 priority rule 针对三条 exact aliases：

```yaml
payload:
  override:
    - models:
        - { name: subscription/codex/gpt-5.6-luna-fast, protocol: codex }
        - { name: subscription/codex/gpt-5.6-sol-fast, protocol: codex }
        - { name: subscription/codex/gpt-5.6-terra-fast, protocol: codex }
      params:
        service_tier: priority
```

只读状态目录查询使用 `Models`，不会读取或写入管理配置；显式选择/创建流程才调用 `ReconcileModels`。Reconciliation 在目录发现前同时取得 `Manager.opMu` 与 DataDir interprocess operation lock，避免旧 snapshot 越过新操作；写入后要求受管新 aliases 出现、旧 ownership 中已移除的 aliases 消失，并连续两次得到同一 raw catalog fingerprint 才算收敛。管理端点没有 ETag/CAS，因此二次 GET/rebase 与 journal 无法让不合作的外部写入者获得真正原子性；若 PUT 后出现未知语义状态，必须 fail closed、保留 recovery journal 且不推进 ownership ledger，不得把未确认状态描述为已应用。


### 4.2 受控 federation 与当前阻塞

`federated` 必须显式提交 consent：canonical provider set、固定 `bounded_work_products` handoff policy、与 workflow 数据依赖完全一致的 cross-provider edges，以及固定 never-transfer 集合 `credentials` / `secrets` / `full_transcripts`。角色 route 必须属于其显式 provider；任意 provider 仅能使用显式 Standard 自锚 route，除非另有独立可信速度变体注册表。每个 concrete route 的 effort 能力仍必须来自 `declared` 或 `probe`。官方 provider 被排除。

当前 Grok `config.toml` 激活路径一次只序列化 active provider 的模型定义与认证边界；非 active provider role 无法在不合并配置/凭据的情况下被安全引用。因此 schema、store、API 和 UI 可以表达并验证 federation consent，但 preview/apply 在生成或写入 artifact 前返回明确 structural blocker。系统不会复制凭据、合并 base URL、切换其他 provider 记忆或声称 federation 已激活。Prompts 只约束最小工作产物，不是硬 DLP 边界。

## 5. 生成物与所有权

默认生成九个用户级 Grok Build artifact：

| 文件 | 语义与关键约束 |
|:---|:---|
| `~/.grok/agents/gbs-terra-coordinator.md` | 注册主协调 `agent_type` |
| `~/.grok/agents/gbs-luna-evidence.md` | 注册任务拆解 `agent_type`，只读 permission mode |
| `~/.grok/agents/gbs-main-implementation.md` | 注册主实现 `agent_type` |
| `~/.grok/agents/gbs-sol-builder.md` | 注册困难实现 / 复核 `agent_type` |
| `~/.grok/roles/gbs-terra-coordinator.toml` | 主协调所选模型/档位、`all` |
| `~/.grok/roles/gbs-luna-evidence.toml` | 任务拆解所选模型/档位、`read-only` |
| `~/.grok/roles/gbs-main-implementation.toml` | 主实现所选模型/档位、`all` |
| `~/.grok/roles/gbs-sol-builder.toml` | 困难实现 / 复核所选模型/档位、`all` |
| `~/.grok/workflows/gbs-max-collab.rhai` | 严格串行、精确预算、无 `parallel()` / `fork_context` / `resume_from` |

当前 Grok Build 只从 `.grok/agents/*.md` / `~/.grok/agents/*.md` 注册自定义 agent type；role TOML 是解析覆盖层，单独存在时 workflow 会报 `Unknown subagent type`。三个沿用的 Terra/Luna/Sol basename 是稳定的 manifest 所有权标识，用来避免升级时遗弃此前由 Switch 管理的文件；schema v4 不把这些名称绑定到特定模型。Renderer 先集中解析四个 assignment，把具体 Standard/Fast alias 与 effort 写入 role TOML，并生成同名 agent definition 注册类型；workflow 使用对应 `agent_type` 与具体 model，不泄露 `speed_tier`、anchor、provider、base URL、凭据或内部 route ID；阶段间只拼接 objective 与最小 work-product 字符串，并显式要求省略 credentials、secrets、无关文件和完整 transcript。普通 package import 或 Switch 启动不会运行这些 artifact。

所有权规则：

- 新 apply 的 manifest 必须恰好包含 `ArtifactPathsForGrokHome` 返回的九个 canonical 绝对路径；为升级已应用的旧版本，只额外接受精确的五文件 legacy canonical 集合，并在下一次 enabled apply 中事务升级；其他空、部分、额外或重定位路径都使状态无效；
- 同名文件不存在：可以创建；
- 同名文件存在但不在 manifest：`ErrArtifactConflict`；
- manifest 中的文件缺失或 hash 不匹配：`ErrArtifactDrift`；
- hash 匹配：可以更新或判定不变；
- symlink、目录或其他非普通文件：拒绝；
- 目录权限为 `0700`，文件权限为 `0600`。

预览后的每个路径会在实际写入前再次核对。若用户或其他进程创建、删除或修改文件，应用拒绝覆盖，并补偿恢复本次已写入的更早 artifact。

## 6. Workflow 层级

| Tier | 顺序 | 精确 `agent_budget` |
|:---|:---|---:|
| Economy | 主协调 | 1 |
| Focused Evidence | 任务拆解 → 主协调 | 2 |
| Focused Build | 主实现 → 主协调 | 2 |
| Assurance | 任务拆解 → 主实现 → 主协调 | 3 |
| Critical | 任务拆解 → 主实现 → 困难实现 / 复核 → 主协调 | 4 |

生产 workflow 在首个 agent 启动前检查 `budget().total == expected_budget`。Grok Build 命名 slash workflow 当前默认使用 128，且 slash autocomplete 不提供该 workflow 的自定义参数选择，因此直接输入 `/gbs-max-collab` 会被有意拒绝。必须由 Grok 通过 workflow tool 传入准确的 1/2/3/4。

`Adaptive` 只保存 Economy-first 的默认提示，不对应一条隐式执行路径。应用提供复制式启动区：用户选择 tier、填写 objective，复制类似“使用 Assurance 运行 gbs-max-collab，目标是：…”的自然语言指令并粘贴到 Grok Build。Grok 必须把它转换为显式 `args.objective`、`args.tier` 和匹配的 `agent_budget`。Critical 不自动触发。生成 workflow 的 metadata 同样说明不能直接使用 named slash launch。

任务拆解阶段返回文件/行号、已确认约束、依赖、工作单元、验收条件、最小测试、风险和下一阶段最小读取集合。主实现消费该工作包，避免重复全仓扫描。困难实现 / 复核在 Critical 路径独立重读实际文件和 diff，可修复确认的本地可逆问题。主协调最后对实际工作区、结果包和测试做收敛与交付。

角色的推理强度由各自生成的 role 文件声明；workflow 调用选择对应 `agent_type` 和已解析模型，不再写死一个全局 `max`。

## 7. Preview、应用与停用

### 7.1 API

- `GET /api/collaboration`：状态、完整 schema-v4 policy 与漂移；enabled/disabled 都检查九个 canonical artifact、文件类型与 hash；
- `POST /api/collaboration/preview`：接收四个 role assignment 和默认 tier，执行无副作用预览；
- `PUT /api/collaboration`：提交同一请求与最新 fingerprint 后应用或停用。

三个端点均为 loopback-only，并经过现有 CSRF、strict JSON、未知字段拒绝和 32 KiB 请求体大小限制；完整 middleware-stack 测试覆盖 paired LAN、恶意 Origin、缺失 token、尾随 JSON 与 oversized body。API 不接受旧三角色或无 `speed_tier` payload 作为新的写入格式；v1/v2 兼容仅存在于持久化 store 的严格读取迁移中。

### 7.2 Enabled apply

```text
strict request
  → load current policy/routing/profile/config
  → resolve four Standard/Fast assignments + validate concrete efforts
  → render desired artifacts
  → plan ownership/hash actions
  → project routing: align default only
  → preview + deterministic fingerprint
  → explicit user confirmation
  → prepare again and compare fingerprint
  → recheck each artifact immediately before write
  → apply artifacts
  → apply config.toml
  → persist routing.json
  → persist collaboration.json
```

Collaboration 只把普通 routing 的 `default` 与 `default_reasoning_effort` 对齐到主协调解析后的**具体 Standard/Fast route**。当前 provider policy 中的 `web_search`、`subagents.explore` 和 `subagents.plan` 原样保留；其他 provider 的记忆策略也不受影响。速度变化会改变 preview fingerprint。后段失败会恢复旧 policy、routing、config 和 artifact。

### 7.3 Disable

停用是纯 policy 操作：

- 只将 policy 标记为 disabled，保留 provider、四角色 anchor/speed/effort assignment 和默认 tier；
- 保留受管文件 manifest 和磁盘 artifact；状态读取仍校验 canonical 路径、文件类型与 hash 漂移；
- 不读取或校验当前 routing 才能完成停用；
- 不改写 `routing.json` 或 `config.toml`；
- 不删除 agent/role/workflow。

自动删除不在当前范围内。未来若增加删除，必须只处理 manifest 记录且 hash 未漂移的文件，并再次取得用户确认。

## 8. UI 行为

UI 只列出当前启用可信 Codex 供应商中的 Standard anchors，并为四个角色分别提供模型、速度与具体 route effort 选择。保存的配置优先于当前推荐值：

- 已保存 Standard anchor 暂时不在当前 provider catalog 时，插入一个禁用且保持选中的“当前不可用”选项；
- 已保存 Fast partner 消失时保留 Fast sentinel，显示不可用且禁止 preview，不回退 Standard；
- 已保存 effort 不再被解析后的具体 Standard/Fast route 支持，或 route capability 来源不再可信时，插入一个禁用且保持选中的“不再支持”选项；
- 表单/请求中空 `speed_tier` 保持为空并由服务端拒绝，不在前端制造 Standard；
- UI 不会静默替换这些值，preview 按钮保持不可用，直到用户显式选择有效替代项；
- policy 停用后仍显示已保存的四角色 assignment 和默认 tier，便于审阅或重新启用；
- 角色可复用同一模型，不进行 distinctness 限制；
- preview 显示 `config.toml`、routing 变化、九个 artifact、warnings 和 fingerprint；
- 启动区根据所选 tier 显示精确 budget，实时生成可复制的自然语言启动指令；objective 为空时拒绝复制，避免发出无目标 workflow；
- federation disclosure 使用独立的信息卡、精确 provider/edge map、传递边界说明和明确的同意复选框；授权覆盖全部五条可执行路径，不随 Default tier 改变。

浏览器/搜索能力不由该卡片推断或重配；它们继续显示和校验普通 routing 的独立能力。

## 9. 用量观察

`internal/cachestats` 只读解析 `~/.grok/logs/unified.jsonl` 中 `shell.turn.inference_done` 的计数字段：

- prompt tokens；
- cached prompt tokens；
- completion tokens；
- reasoning tokens；
- turn 与 cache hit rate。

它不读取 transcript。当前 inference event 不直接携带 model ID，模型名称来自 session `summary.json`；如果一个 session 中途切换模型，历史 turn 可能被归到摘要中的当前模型。因此当前面板适合粗粒度观察，不是精确计费账本。

Switch 不硬编码任何角色或模型的价格，也不把 subscription token 换算成美元。只有用户提供可靠价格/配额权重并完成真实任务试点后，才可以讨论相对成本。

## 10. 验证状态

当前本地测试覆盖：

- domain：四角色 Standard anchor/speed/effort 结构、exact trusted registry、同 provider、重复 anchor、Standard/Fast 解析、缺失/歧义/伪造 Fast fail closed、具体 route effort capability；
- migration/store：严格 v1/v2/v3→v4 映射、旧具体 Fast ID 不推断、Snapshot 不重写、显式 Replace 保存 v4、disabled metadata/manifest 保留、畸形旧输入 fail closed；
- renderer：五个确定性 artifact、四个 role 的具体 Standard/Fast alias 与 effort、无内部 speed/provider 元数据、无 `resume_from`、串行顺序、tier guard 和精确预算；
- managed ownership/status：unmanaged collision、canonical exact manifest、missing/hash drift、symlink/non-regular、disabled retained drift、写前竞态、私有权限和部分失败回滚；
- HTTP：完整 middleware-stack loopback/LAN/CSRF/strict/oversize、完整四角色 v4 payload、preview 无副作用、confirmation/fingerprint、速度变更 fingerprint、concrete default 对齐、`web_search`/`explore`/`plan` 保留、完整补偿和 policy-only disable；
- UI：Standard-anchor-only 选择、每角色速度解析与 concrete effort、缺失 Fast sentinel、空 speed 不补 Standard、保存的缺失 anchor / 不受支持 effort 保留、preview/apply/disable；
- workflow path mapping：五条 tier 的角色组合和预算在 renderer 单元测试中逐条覆盖。

Grok workflow tool 的 `validate_only` 会校验 metadata、编译和一条 canned-host 路径，不发起真实模型调用；它并不证明其他分支或 live host 行为。2026-08-04 的 top-level 尝试确认生产脚本可编译，但 canned host 没有采用请求中的精确 budget，因而触发预期的 fail-closed 门槛；不能把五条路径记为 path-specific PASS，也不得为适配 canned host 削弱生产预算守卫。

2026-08-04 另以精确 `agent_budget=1` 完成一次 Economy 最小只读 live smoke：workflow 成功，只调用主协调，没有额外 child agent、文件修改或外部动作。这证明 Economy 路径能在真实 workflow host 上按精确预算启动，但不证明其他 tier、Fast priority、跨角色 handoff、成本节省或质量优势。

## 11. 尚未证明与后续门槛

当前尚未：

- 完成五条生产 workflow 的 top-level path-specific `validate_only` PASS（当前 canned-host budget 行为与生产精确门槛不兼容）；
- 运行 Focused Evidence、Focused Build、Assurance 与 Critical 的 live workflow/model smoke；
- 证明真实 provider 对四个角色所选具体 Standard/Fast route 与 effort 都按预期执行、Fast 确实请求 priority 且没有静默 fallback；
- 完成 10–20 个真实任务的质量/用量对照；
- 证明固定美元节省、准确 per-agent 计费或通用模型优劣。

任何 live smoke 都会消耗额度，Fast 还可能提高每次调用的 subscription credit 消耗，均需单独授权。宣称节省前至少比较 Standard/Fast 的平均额外 child、prompt/reasoning token、首次测试通过率、返工率、人工修正次数与可靠的 credit 观测；若某种四角色配置的返工或 priority 消耗抵消调用节省，应按任务类别调整角色模型、速度、effort 或 tier 门控，而不是无条件推广。