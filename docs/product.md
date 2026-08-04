# Grok Build Switch 产品文档

> 面向用户与维护者的当前产品边界、数据和使用说明。最后更新：2026-08-03。

---

## 1. 产品定位

Grok Build Switch 是一个本地 macOS 菜单栏/桌面工具，用于管理 Grok CLI 的官方登录路由、`~/.grok/config.toml`、普通供应商 Profile、统一模型路由和可选的 Grok Build Max Collaboration 配置预设。

核心能力：

- 通过 Grok CLI 官方流程登录，并在登录后应用官方模型路由；
- 以普通 Profile 管理供应商、Base URL、API Key、上游格式和常用模型；
- 统一配置 `default`、`web_search`、`subagents.explore`、`subagents.plan`；
- 为主协调、任务拆解、主实现、困难实现 / 复核分别选择可信 Codex 订阅供应商内的 Standard 模型锚点、Standard/Fast 速度档与推理强度，预览并管理四角色 role/workflow，用精确串行预算减少不必要的 agent 调用；
- 只读展示近期 prompt、cached prompt、completion、reasoning token 和缓存命中率；
- 通过内嵌 CLIProxyAPI 接入受支持的订阅代理；
- 查看、校验和编辑 `~/.grok/config.toml`；
- 提供 LAN 配对访问和 SSH 连接管理；
- 通过 macOS 菜单栏或 Wails 窗口提供管理界面。

### 1.1 当前边界

本节核心能力清单是当前产品的完整范围。清单之外的旧扩展已移除，不再作为当前功能、接口或数据目录列出。

历史博客和历史设计文档可能描述旧版本能力，它们只用于版本追溯，不代表当前产品行为。

---

## 2. 系统架构

```text
Browser management UI / Wails / macOS menu bar
                       │
                       ▼
                  HTTP Server
                       │
       ┌───────────────┼────────────────────┐
       ▼               ▼                    ▼
    Profiles        Routing          Collaboration / Services
 ordinary data   unified policy     roles/workflow + subscription/LAN/SSH
                       │                    │
                       ▼                    ▼
             ~/.grok/config.toml      ~/.grok/roles + workflows
```

### 2.1 配置真相

- 普通 Profile 保存可选供应商和模型定义；
- 统一路由策略保存当前模型选择；
- `collaboration.json` 保存独立的预设状态和受管文件 SHA-256 manifest，不保存凭据或 agent 运行状态；
- `~/.grok/config.toml` 是 Grok CLI 实际执行配置；
- 官方认证由 Grok CLI 官方登录流程管理；
- 路由更新必须保持统一路由策略与 `config.toml` 一致。

### 2.2 桌面入口

- **macOS 菜单栏**：打开管理界面、查看状态和执行常用切换；
- **Wails 窗口**：提供桌面管理界面；
- **本地浏览器管理页**：与桌面入口共享服务端能力；
- **LAN**：经配对后访问受保护的管理功能；
- **SSH**：管理远程连接和文件操作。

---

## 3. 核心功能

| 功能 | 当前行为 |
|:---|:---|
| 官方 Grok CLI | 使用官方登录流程，验证登录状态并应用官方模型路由 |
| 普通 Profile | 管理供应商、Base URL、API Key、上游格式和常用模型 |
| 统一模型路由 | 管理 default、web_search、explore 和 plan |
| Max Collaboration | 校验同一可信 Codex 订阅供应商中四个独立角色的 Standard 锚点、Standard/Fast 速度档与推理强度，预览并生成用户级 role/workflow；不运行 agent |
| 用量观察 | 聚合近期 prompt/cached/completion/reasoning token、turn 和缓存命中率；不推算美元成本 |
| 订阅代理 | 管理内嵌 CLIProxyAPI 的生命周期、登录和代理路由 |
| `config.toml` 编辑 | 查看、校验并编辑 Grok CLI 当前配置 |
| LAN | 提供配对、来源检查和 CSRF 保护 |
| SSH | 管理连接及远程文件操作 |
| 菜单栏 / Wails | 提供本地桌面入口和状态控制 |

### 3.1 普通 Profile

Profile 用于保存一个上游所需的基础信息：

- 显示名称；
- Base URL；
- API Key；
- 上游协议或格式；
- 可用于统一路由的模型列表。

Profile 保持普通、可理解的基础模型选择界面。

### 3.2 统一模型路由

统一路由包含：

```text
official
default
web_search
subagents.explore
subagents.plan
```

路由更新执行严格校验、目标配置预览、原子写入和失败回滚。该流程只处理配置与模型选择。

### 3.3 Max Collaboration

该预设提供四个独立语义角色：

- **主协调**：范围控制、结果收敛、最终验证与交付；
- **任务拆解**：只读探索并输出约束、依赖、文件和测试组成的紧凑工作包；
- **主实现**：完成有边界的代码修改和聚焦验证；
- **困难实现 / 复核**：处理高难实现、调试、对抗性复核与关键修复。

Collaboration schema v5 为每个角色分别保存同一可信 Codex 订阅供应商中的 **Standard route anchor**、显式 `speed_tier` 和推理强度；允许多个角色复用同一锚点。Standard 使用原有逻辑模型身份，不注入 priority。Fast 解析为同一物理模型的精确 `subscription/codex/<model>-fast` 别名，由 CLIProxy 注入 `service_tier: priority`；它通常更快，但会消耗更多订阅 credits，且没有可可靠声称的固定倍率。

Standard/Fast 关系只信任 Switch exact registry 中 Terra/Sol/Luna 的显式同供应商元数据，不从后缀、显示名、provider 名或 GPT 名称推断。Fast partner 缺失、歧义或伪造时直接失败，不静默回退。推理强度在解析后的具体 Standard/Fast route 上校验；只有该 route 明确声明或经用户授权 probe 证明支持所选 effort 时才可应用。未知 capability 会 fail closed。

schema v1/v2 读取时保留其 Standard 速度语义，schema v3 保留已保存的 Standard/Fast 选择，schema v4 迁移旧预算与并发控制；四者都只在内存迁移为 v5，读取不会重写旧字节，下一次显式保存才持久化 v5。若 v1/v2 旧记录本身保存了具体 `-fast` route ID，它不会被自动推断为 Fast；无法解析成可信 Standard 锚点时要求用户显式修复，以避免迁移意外提高 credit 档。

独立 workflow 顶层阶段串行；由 workflow 顶层启动 10 个主实现 agent：

| Tier | 顺序 | 精确 `agent_budget` |
|:---|:---|---:|
| Economy | 主协调 | 1 |
| Focused Evidence | 任务拆解 → 主协调 | 2 |
| Focused Build | 10 个主实现 agent → 主协调 | 11 |
| Assurance | 任务拆解 → 10 个主实现 agent → 主协调 | 12 |
| Critical | 任务拆解 → 10 个主实现 agent → 困难实现 / 复核 → 主协调 | 13 |

当前 workflow 不使用 `resume_from`。生成脚本会拒绝 Grok Build workflow 的默认 128 预算；调用方必须显式选择 tier，并通过 workflow tool 传入完全匹配的预算。`Adaptive` 只是控制面提示，不会自动启动 workflow；Critical 也不会自动进入。

应用流程为 preview → fingerprint → 用户确认 → 受管 artifacts → `config.toml` → `routing.json` → `collaboration.json`，后段失败会补偿回滚。受管文件是四个 agent definition、四个 role 和一个 workflow；agent definition 注册自定义 `agent_type`，role TOML 保存模型、推理强度与默认 capability。新 manifest 必须恰好覆盖九个 canonical Grok Home 路径；既有精确五文件 legacy manifest 只作为下一次 enabled apply 的事务升级源，三个旧 role basename 仅为延续所有权而保留，不再绑定特定模型。文件只在当前 SHA-256 仍匹配 manifest 时更新，并在实际写入前再次检查；未知同名文件、缺失/漂移文件、并发用户编辑、符号链接或其他非普通文件都拒绝覆盖。

Collaboration 只把普通 routing 的 `default` **具体 Standard/Fast route**与默认推理强度对齐到主协调；`web_search`、`explore` 和 `plan` 保持原选择，浏览器/搜索能力与 Collaboration 正交。停用只将 policy 标记为 disabled，并保留 provider、四角色锚点/速度档/effort、默认 tier 与 manifest；不删除 agent/role/workflow，也不改写 routing 或 `config.toml`。停用后的状态仍检查保留 artifact 的 canonical 路径、文件类型和 hash 漂移。

Switch 只做配置和路由控制面。真实 agent、消息、workflow 运行与预算消耗均属于 Grok Build 执行面；Switch 不保存 transcript、chat 或 session graph。

### 3.4 官方路由

- 未登录时，应用调用 Grok CLI 官方登录流程；
- 登录完成后，用户可再次选择官方路由；
- 应用不得以自身 DataDir 记录替代或托管 Grok CLI 官方认证；
- 应用的旧记录清理不得删除、重置或改写官方认证。

### 3.5 订阅代理

订阅代理由内嵌 CLIProxyAPI 提供。订阅代理账号、令牌和相关凭据属于保留功能的运行数据，不属于本轮遗留清理目标。

### 3.6 `config.toml` 编辑

应用允许用户查看、校验和编辑 `~/.grok/config.toml`。统一路由写入与手动编辑都必须尽量保持配置可解析，并在失败时避免留下半写入状态。

---

## 4. 数据与安全

### 4.1 数据分类

| 数据 | 所属 | 清理边界 |
|:---|:---|:---|
| `~/.grok/config.toml` | Grok CLI 执行配置 | 保留；仅按用户配置操作更新 |
| Grok CLI 官方认证 | Grok CLI 官方登录 | 禁止作为应用旧记录删除或改写 |
| 普通 Profile | 当前产品 | 保留 |
| 统一路由策略 | 当前产品 | 保留 |
| `collaboration.json` | 当前产品 | 保留；仅记录 policy、模型引用和受管文件 hash |
| `~/.grok/agents/gbs-*.md`、`~/.grok/roles/gbs-*.toml`、`~/.grok/workflows/gbs-max-collab.rhai` | Grok Build 用户级配置 | 停用时保留；未知或已漂移文件禁止覆盖 |
| `~/.grok/logs/unified.jsonl` 的用量字段 | Grok Build 运行日志 | 只读聚合，不展示 transcript |
| 订阅代理账号与凭据 | 当前产品 | 保留，禁止纳入旧记录清理 |
| LAN 配对和 SSH 配置 | 当前产品 | 保留 |
| 应用设置和日志 | 当前产品运行数据 | 按各自策略处理 |
| 已移除能力的旧 DataDir 记录 | 旧产品能力 | 用户已授权清理，但必须确认归属 |

### 4.2 已授权的遗留清理

用户已授权清理已移除能力遗留在应用 DataDir 中的旧记录。该授权是范围明确的清理授权，不是删除整个 DataDir 的授权。

执行清理时：

1. 只删除能够确认归属于已移除能力的应用自有记录；
2. 明确排除 Grok CLI 官方认证；
3. 明确排除订阅代理账号、令牌和凭据；
4. 明确排除普通 Profile、统一路由、Collaboration Policy/受管 role/workflow、LAN、SSH 和当前应用设置；
5. 无法确认归属时保留并报告。

### 4.3 通用安全要求

- 默认只监听本机地址；
- LAN 请求必须经过配对、来源和 CSRF 检查；
- 状态响应和日志不得暴露 API Key、OAuth token 或私有 header；
- 敏感文件应使用用户私有权限和原子写入；
- 不向 Git 提交本机凭据、运行数据或构建产物。

---

## 5. 使用流程

### 5.1 使用官方 Grok CLI

1. 在应用中选择官方 Grok CLI；
2. 如尚未登录，完成 Grok CLI 官方登录流程；
3. 返回应用并重新应用官方路由；
4. 在终端启动 Grok CLI 验证配置。

### 5.2 使用普通 Profile

1. 新建 Profile；
2. 填写名称、Base URL、API Key 和上游格式；
3. 获取或填写模型列表；
4. 保存 Profile；
5. 在统一模型路由中选择 default、web_search、explore 和 plan；
6. 保存并检查 `~/.grok/config.toml`。

### 5.3 配置 Max Collaboration

1. 先启用一个提供所需模型路由的自定义供应商；
2. 为主协调、任务拆解、主实现、困难实现 / 复核分别选择 Standard 模型锚点、Standard/Fast 速度档和推理强度；允许复用同一锚点；
3. 确认所选 Fast partner 可信且存在，并确认每个解析后的具体 route 都明确声明或经用户授权 probe 证明支持该角色所选 effort；然后选择默认提示 tier；
4. 点击“预览变更”，检查 `config.toml`、routing、四个 agent definition、四个 role 和一个 workflow；预览中只有 routing `default` 的具体 Standard/Fast route / 默认推理强度可随主协调改变；
5. 确认 fingerprint 未过期后应用；
6. 需要真实执行时，在 Grok Build 中用 workflow tool 启动 `gbs-max-collab`，显式传入 `objective`、tier 和精确 `agent_budget`。

应用本身不会启动模型调用。停用只停止使用 policy，并保留已生成文件。真实 live smoke、成本试点或订阅额度消耗需另行授权。

### 5.4 使用订阅代理

1. 打开订阅代理管理；
2. 按受支持流程登录订阅账号；
3. 确认代理状态正常；
4. 将代理提供的模型加入统一路由。

### 5.5 使用 LAN、SSH 和桌面入口

- 通过菜单栏快速打开应用和查看状态；
- 使用 Wails 窗口进行完整管理；
- 开启 LAN 前完成配对并确认网络边界；
- 通过 SSH 配置连接后执行受支持的远程操作。

---

## 6. 构建与验证

系统目标：macOS 15+，Apple Silicon / arm64。

```bash
./build-macos.sh
```

常规验证应覆盖：

- 普通 Profile 与统一路由；
- Grok CLI 官方登录和官方路由；
- Max Collaboration schema v5 的 exact Standard/Fast 解析、具体 route effort fail-closed、preview/fingerprint、canonical manifest/文件类型/hash 漂移、事务回滚、policy-only disable 和 tier 精确预算；
- CLIProxy 完整 YAML ownership merge 与仅精确 Fast alias 的 `service_tier: priority` 整形；
- 用量聚合中的 prompt/cached/completion/reasoning token；
- 订阅代理；
- `config.toml` 编辑；
- LAN、SSH、菜单栏和 Wails；
- DataDir 清理不触及官方认证或订阅代理凭据。

构建产物位于 `dist/`，不应提交 Git。

---

## 7. 常见问题

### 官方路由无法启用

确认 Grok CLI 已安装，并完成其官方登录流程。应用不会用自身托管的认证记录替代官方登录。

### 路由保存失败

检查 Profile、模型选择、`~/.grok/config.toml` 写权限和统一路由存储状态。失败后确认配置未处于半写入状态。

### web_search 路由不可用

请选择实际支持原生搜索的模型，或在 Grok CLI 自身配置所需 MCP。应用不会把不支持搜索的模型描述为可用。

### Max Collaboration 无法预览或应用

确认当前启用的是 Switch 管理的可信 Codex 订阅供应商，四个角色都选择了 Standard 锚点、速度档与推理强度，并且解析后的具体 Standard/Fast route 对所选 effort 的能力来源是 `declared` 或 `probe`。角色可以复用同一锚点。若已保存锚点暂时缺失、Fast partner 消失，或 effort 不再受支持 / 不再具备可信 capability 来源，UI 会保留禁用的原选择，需由用户显式替换；若提示 non-canonical manifest、unmanaged collision、symlink 或 drift，请不要直接覆盖，先判断文件是否已由用户接管。

### 是否已经证明节省了多少钱

没有。当前面板报告调用事件和 token；订阅定价、配额和质量返工成本未被硬编码。真实多任务试点不作为当前版本完成门槛，未来只有在另立研究项目并获得充分证据后才能评估总交付成本；不能仅凭本地 `validate_only` 或代理 benchmark 宣称固定货币节省。

### 能否清理旧 DataDir 记录

可以。用户已授权清理已移除能力的应用自有旧记录，但必须保留 Grok CLI 官方认证和订阅代理凭据，并保留无法确认归属的数据。


> Collaboration schema v5 defaults to `single_provider`. `federated` is an explicit-consent preview model with per-role provider and data-scope assignments; current active-provider/config serialization blocks safe multi-provider activation, so the Switch fails closed rather than merging credentials or pretending cross-provider routing works.
