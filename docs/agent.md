# Grok Build Switch — 维护者文档

> 面向维护者的当前架构、产品边界与数据安全约定。最后更新：2026-08-04。

---

## 1. 项目定位

Grok Build Switch 是一个 macOS 桌面应用，用于管理 Grok CLI 官方登录路由、普通 Profile、统一模型路由、Max Collaboration 配置预设、订阅代理和 `~/.grok/config.toml`，并提供 LAN、SSH、菜单栏与 Wails 入口。

### 1.1 真相源与工作区边界

- GitHub `CyberStaZJU/grok-build-switch` 是代码、测试、轻量文档和发布记录的权威源。
- 本地未提交修改在 commit/push 前是不可替代的工作，不得重置或覆盖。
- 构建产物、日志、凭据和运行配置不进入 Git。
- 修改当前产品文档时，不回写历史博客或历史设计文档。

### 1.2 当前产品边界

保留能力：

- Grok CLI 官方登录与官方路由；
- 普通 Profile；
- default、web_search、explore、plan 统一模型路由；
- Max Collaboration 配置控制面与 token/cache 用量观察；
- CLIProxyAPI 订阅代理；
- `config.toml` 查看、校验与编辑；
- LAN、SSH、macOS 菜单栏和 Wails。

以上清单是完整产品范围。清单之外的旧扩展已移除；维护者不得在无新产品决策时重新接线，也不得在当前说明中保留其功能、接口或数据目录清单。

---

## 2. 入口与核心职责

| 入口 | 作用 |
|:---|:---|
| 默认应用入口 | 启动本地 HTTP 服务、菜单栏和浏览器管理页 |
| Wails 入口 | 启动桌面窗口并复用同一服务能力 |
| 菜单栏 | 打开窗口、查看状态和执行常用路由操作 |

两个桌面入口都应只初始化当前保留能力。入口行为必须保持一致，不得因构建标签重新暴露已移除范围。

核心职责可以概括为：

```text
Profiles ─┐
          ├─> unified routing ─> ~/.grok/config.toml
Official ─┘

Collaboration policy ─> ~/.grok/agents/gbs-*.md
                     ├> ~/.grok/roles/gbs-*.toml
                     └> ~/.grok/workflows/gbs-max-collab.rhai
Usage reader          ─> ~/.grok/logs/unified.jsonl (read-only fields)
Subscription proxy   ─> supported proxy routes
LAN / SSH            ─> protected remote management
Menu bar / Wails     ─> local desktop access
```

---

## 3. 路由事务

路由更新应遵守：

```text
HTTP request
  → strict decode
  → merge current policy
  → validate profile/model selection
  → preview target config
  → atomically write config.toml
  → persist routing policy
  → roll back this write on persistence failure
```

重要约束：

- `routing.json` schema v2 保存唯一 `active_provider_id`、稳定的 `provider_id:model` 引用和每个供应商的记忆策略；
- default、web_search、explore、plan 必须全部属于启用供应商，不能重新引入跨供应商会话图；v1 跨供应商可选字段仅迁移进各自供应商的策略记忆；
- 自定义非空 web_search 还必须是 `responses` 后端且明确支持后端搜索；UI 过滤不是安全边界，服务端拒绝无能力路由且不得产生持久化副作用；
- 有自定义供应商时必须存在启用项；启用供应商不能删除，需先启用另一个供应商；
- 自定义供应商切换保留 `config.toml` 的组合自定义模型目录，以兼容旧会话固定的旧别名；
- 官方供应商是互斥特例：使用 Grok CLI 官方登录，切换时清除自定义模型定义和认证，不允许混合；
- 路由更新只处理配置与模型选择；普通 Profile UI 只管理基础连接信息和常用模型；
- UI 不超出当前产品范围。

---

## 4. Collaboration 控制面

### 4.1 真相与边界

- `routing.json` 继续使用 schema v2；不要为本功能新增 routing v3 或 general-purpose 路由槽。
- `collaboration.json` 使用独立 schema v5，保存 provider ID、四个语义角色各自的稳定 Standard route anchor、显式 `speed_tier` 与 effort、默认提示 tier、1/2/11/12/13 budget、10 个顶层主实现 agent / 重试限制及受管 artifact manifest。
- schema v1/v2/v3/v4 只作为严格迁移输入：v1 coordinator → 主协调与主实现、evidence → 任务拆解、builder → 困难实现 / 复核，全局 effort 复制到四角色；v2 保留四角色 model/effort 并固定 Standard；v3 保留四角色 anchor/speed/effort。四者都迁移到 v5 single-provider，复制顶层 provider 到各角色、写入 workflow 派生的固定 data scope，并保持 federation consent 为空。Snapshot 不重写旧字节，显式 Replace 才保存 v5。旧具体 `-fast` route ID 不按后缀推断为 Fast；若无法作为可信 Standard anchor 解析就 fail closed，要求显式修复，避免意外提高 credit 档。
- policy 不保存 API Key、消息、transcript、session graph、agent ID 或 workflow 运行状态。
- Switch 只生成 Grok Build 用户级配置；Grok Build 负责实际 `agent()` 和预算消耗。当前生成 workflow 不使用 `resume_from`。

### 4.2 角色与 workflow 不变量

- 主协调、任务拆解、主实现、困难实现 / 复核都是独立配置；可以复用同一 Standard anchor，但每个角色必须单独保存 `speed_tier` 和 reasoning effort，二者不可互相推断。
- Standard route 必须显式自锚定，使用 exact-registry 可信 `subscription/codex/<physical-id>` 别名且不注入 priority。Fast route 必须显式锚定同 provider Standard route，并精确解析为 `<standard-alias>-fast`；只有 `gpt-5.6-terra`、`gpt-5.6-sol`、`gpt-5.6-luna` 在当前 registry 中可信。禁止按后缀、显示名、provider 名或 GPT 名称推断关系。
- Fast 通过 CLIProxy 受管完整 YAML 合并，仅对精确可信 Fast aliases 注入 `service_tier: priority`。Standard 不得注入。缺失/歧义/伪造 Fast 必须失败，绝不静默回退；Fast 通常更快且消耗更多订阅 credits，但文档/UI 不声称固定倍率。
- `gbs-terra-coordinator`、`gbs-luna-evidence` 与 `gbs-sol-builder` 是为保持旧 manifest 所有权而保留的稳定 basename，不再绑定 Terra/Luna/Sol 模型；`gbs-main-implementation` 是第四个 role 文件。
- capability 分别为：主协调 `all`、任务拆解 `read-only`、主实现 `all`、困难实现 / 复核 `all`。
- `gbs-max-collab.rhai` 不使用 `parallel()`、`fork_context` 或 `resume_from`，各 tier 串行路径为：Economy=主协调；Focused Evidence=任务拆解→主协调；Focused Build=主实现→主协调；Assurance=任务拆解→主实现→主协调；Critical=任务拆解→主实现→困难实现 / 复核→主协调。
- 生产脚本必须对 `budget().total` 做精确相等检查，拒绝默认 128。named slash launch 当前不能携带自定义 budget 或弹出参数选择；UI 与 workflow metadata 必须引导用户复制 objective/tier 指令，让 Grok 通过 workflow tool 传入精确预算。不要为了让 `validate_only` canned host 通过而削弱生产守卫；五条路径应分别验证，并明确 `validate_only` 只覆盖编译和所选 canned-host 路径。

### 4.3 能力与事务

- 四个 Standard anchor 必须属于当前启用的同一 `subscription-proxy:codex` 可信供应商；角色之间允许复用 anchor。未来跨供应商联邦必须是单独的显式 opt-in 设计，不得通过本 schema 偷渡。
- 每个所选 effort 在解析后的具体 Standard/Fast route 上校验，只信任 `supports_reasoning_effort=true`、`reasoning_efforts_source ∈ {declared, probe}` 且支持列表显式包含该档位；不得从锚点、模型名或全局默认推断。
- preview 必须无副作用；apply 必须使用最新 fingerprint 和显式确认。
- artifact 写入前后均以 SHA-256 manifest 判断所有权，并在每个实际写入前复核，防止 stale preview 覆盖用户编辑；manifest 必须**恰好**覆盖 Grok Home 下四个 agent definition、四个 role 与一个 workflow 的九个 canonical 路径；升级时只额外接受此前精确的五文件 legacy manifest，并在下一次 enabled apply 中事务升级。状态读取在 enabled/disabled 两种 policy 下都拒绝空/部分/额外/重定位 manifest、symlink 和其他非普通文件，并报告 hash 漂移。
- enabled apply 顺序为 artifacts → config → routing → policy；失败时按 policy/routing/config/artifacts 补偿。它只能把 active provider 的 `default` 与默认 effort 对齐到主协调，必须保留 `web_search`、`explore` 与 `plan`。
- disable 必须 policy-only，只切换 enabled 并保留 provider、四角色 assignment、默认 tier 与 manifest；即使 routing/config 已漂移或 routing 不可读也不得借机修复、覆盖或删除其他状态。
- Collaboration API 全部 loopback-only，并复用 strict JSON、CSRF、请求体限制和现有锁顺序（`collaborationMu` → `routingMu`）。
- UI 模型 selector 只列 Standard anchors，并按每角色速度档解析具体 route；必须保留已保存但暂时缺失的 anchor、消失的 Fast partner，以及具体 route 不再支持或不再具备可信 capability 来源的 effort，以 disabled selected option 展示。空 speed 值必须保持为空，不得制造 Standard；停用 policy 后也应继续展示所保存的四角色 assignment 和默认 tier。
- 浏览器/搜索能力保持正交；Collaboration 不为模型补充 web capability，也不接管普通 web_search 路由。

### 4.4 用量边界

`internal/cachestats` 只解析 `shell.turn.inference_done` 的计数字段，聚合 prompt、cached prompt、completion、reasoning token 与 hit rate。不要读取或展示消息正文。当前事件不直接提供 model ID，按 session summary 归属只是一种近似，文档和 UI 不得把它描述为精确 per-turn 成本。

---

## 5. 数据边界与清理授权

### 5.1 保留数据

以下数据属于当前产品或 Grok CLI，必须保留：

- `~/.grok/config.toml`；
- Grok CLI 官方认证；
- 普通 Profile；
- 统一路由策略；
- Collaboration Policy、受管 agent/role/workflow 和 Grok Build 用量日志；
- 订阅代理账号、令牌和凭据；
- LAN、SSH 和当前应用设置。

### 5.2 已授权清理

用户已明确授权清理已移除能力遗留在应用 DataDir 中的旧记录。实现或执行清理时必须使用可审计的范围判断：

1. 仅处理能够确认属于已移除能力的应用自有记录；
2. 将 Grok CLI 官方认证列入硬性排除项；
3. 将订阅代理账号、令牌和凭据列入硬性排除项；
4. 不以整个目录为单位扩大删除范围；
5. 对未知文件或混合数据停止自动删除并报告。

不要在当前文档中保留已移除能力的具体旧路径或接口清单；清理实现应以代码中的受控迁移规则和测试为准。

---

## 6. HTTP 与安全边界

当前 HTTP 服务只应暴露保留功能所需的管理和代理能力。新增或修改状态的端点必须遵守：

- 默认本机监听；
- LAN 配对与可信来源检查；
- 修改请求的 CSRF 防护；
- 请求体大小限制和严格解码；
- 响应脱敏，禁止返回 API Key、OAuth token 或私有 header；
- 不新增已移除能力的兼容端点。

---

## 7. 前端约定

- 当前 UI 聚焦官方登录、普通 Profile、统一模型路由、Max Collaboration、用量观察、订阅代理、配置编辑、LAN 和 SSH。
- 菜单栏与 Wails 必须提供一致的当前能力。
- UI 入口、状态卡和表单只覆盖当前产品范围。
- 非原生搜索模型不得被描述为自动获得额外搜索工具。
- 历史博客与设计文档不作为当前 UI 需求来源。

---

## 8. 构建与验证

常规变更至少验证：

```bash
node --check ui/app.js
node --test ui/app_behavior_test.js
go test ./...
go vet ./...
go test -race ./...
go test -tags wailsgui .
```

产品边界相关变更还应覆盖：

- 官方登录和官方路由；
- 普通 Profile 与统一路由事务；
- Max Collaboration schema v5 exact Standard/Fast 解析、concrete effort capability、preview/fingerprint、canonical artifact ownership/type/drift/race、跨文件回滚、policy-only disable 和 workflow validate-only；
- CLIProxy 完整 YAML ownership merge、canonical ledger/认证 marker、二次 GET rebase、write-ahead recovery journal、跨进程 operation lock、受管 alias 出现/消失的稳定目录收敛、只读 `Models` 与显式 `ReconcileModels` 分离、Standard 无 priority 与精确 Fast `service_tier: priority`；
- completion/reasoning token 的 aggregate、recent 和 UI 展示；
- 订阅代理及其凭据保留；
- `config.toml` 编辑；
- LAN、SSH、菜单栏和 Wails；
- DataDir 遗留清理不会触及 Grok CLI 官方认证或订阅代理凭据；
- 当前 UI 和当前产品文档不出现已移除能力。

发布前运行构建、签名/公证检查和安装包 smoke test。构建产物必须留在 `dist/` 或仓库外，不提交 Git。真实 workflow/model smoke、写入真实 `~/.grok`、推送、发布和删除外部 evidence 都是单独授权动作；本地单元测试与 `validate_only` 不替代这些确认。


> Collaboration schema v5 defaults to `single_provider`. `federated` is an explicit-consent preview model with per-role provider and data-scope assignments; current active-provider/config serialization blocks safe multi-provider activation, so the Switch fails closed rather than merging credentials or pretending cross-provider routing works.
