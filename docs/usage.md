# 使用教程

## 1. 准备 Grok CLI

从 [Grok Build](https://grok.com/build) 安装 Grok CLI。需要使用官方账号时，请通过 Grok CLI 官方登录流程完成登录；Grok Build Switch 不托管或替代官方认证。

## 2. 启动 Grok Build Switch

在 Apple Silicon Mac 上构建并打开 `Grok Build Switch.app`。应用可从 macOS 菜单栏、Wails 桌面窗口或本地浏览器管理页使用。

## 3. 使用官方 Grok CLI 路由

1. 在应用中选择官方 Grok CLI；
2. 如果尚未登录，按提示完成 Grok CLI 官方登录；
3. 登录完成后返回应用，再次应用官方路由；
4. 在终端运行 `grok` 验证当前模型和配置。

应用旧记录清理不会删除或改写 Grok CLI 官方认证。

## 4. 添加普通 Profile

1. 打开 Profile 管理；
2. 填写名称、Base URL、API Key 和上游格式；
3. 拉取或填写该上游可用模型；
4. 保存 Profile。

普通 Profile 用于基础连接和常用模型选择。

## 5. 配置统一模型路由

在模型路由页面分别选择：

- default；
- web_search；
- explore；
- plan。

保存后，应用会校验选择并更新 `~/.grok/config.toml`。如写入失败，应确认原配置仍可解析，并检查 Profile、模型和文件权限。

## 6. 配置 Max Collaboration

Max Collaboration 是 Grok Build 配置预设，不是 Switch 内部的 agent runtime。

1. 在统一路由中启用一个自定义供应商；
2. 在 **Max Collaboration** 卡片中为四个语义角色分别选择 Standard 模型锚点、速度档和推理强度：
   - 主协调；
   - 任务拆解；
   - 主实现；
   - 困难实现 / 复核；
3. 角色可以复用同一 Standard 锚点；速度档与推理强度相互独立：
   - **Standard**：使用原有逻辑模型身份，不请求 priority；
   - **Fast**：解析为同一物理模型的显式 `-fast` 逻辑别名，由 CLIProxy 请求 `service_tier: priority`，通常更快但消耗更多订阅 credits；
4. Fast 只对 Switch exact registry 中可信 Terra/Sol/Luna 配对开放。若 Fast partner 缺失、歧义、关系不可信或具体 Fast route 不支持所选 effort，预览直接失败，不静默回退 Standard；
5. 每个具体 Standard/Fast route 都必须由 `declared` 或用户授权的 `probe` 明确证明支持该角色所选推理强度；
6. 选择默认提示层级；
7. 点击“预览变更”，检查目标 `config.toml`、routing、四个 agent definition、四个 role 和一个 workflow。agent definition 注册 workflow 的 `agent_type`；role 保存模型、推理强度与默认 capability。只有 routing `default` 的**具体 Standard/Fast route**与默认推理强度会对齐主协调，`web_search`、`explore` 和 `plan` 保持不变；
8. 点击“确认并应用预览”。fingerprint 过期、manifest 既不是九个当前 canonical 路径也不是可升级的精确五文件 legacy manifest、同名未知文件、受管文件缺失/漂移、符号链接或非普通文件时，应用会拒绝写入。

如果已保存的 Standard 锚点暂时不在当前 provider catalog 中、所选 Fast partner 消失，或已保存 effort 不再受具体 route 支持 / capability 来源不再可信，界面会把原选择保留为禁用项，不会静默替换或降级；请显式选择新的有效值后重新预览。

旧 schema v1/v2 policy 在读取时保留 Standard 速度语义，schema v3 保留已保存的 Standard/Fast 选择，schema v4 还会把旧预算和并发控制迁移为新默认；四者都只在内存迁移为 v5，读取不改写原文件，下一次显式保存才持久化 v5。v1/v2 不会自动把带 `-fast` 的旧具体 route ID 推断成 Fast；无法作为可信 Standard 锚点解析时会 fail closed，需用户显式选择锚点与速度档，以避免迁移时意外提高 credit 档。

层级与真实 workflow 预算：

| Tier | 执行顺序 | `agent_budget` |
|:---|:---|---:|
| Economy | 主协调 | 1 |
| Focused Evidence | 任务拆解 → 主协调 | 2 |
| Focused Build | 10 个主实现 agent → 主协调 | 11 |
| Assurance | 任务拆解 → 10 个主实现 agent → 主协调 | 12 |
| Critical | 任务拆解 → 10 个主实现 agent → 困难实现 / 复核 → 主协调 | 13 |

当前 workflow 不使用 `resume_from`。`Adaptive` 只是 Economy-first 的配置提示，Switch 不会自动运行 workflow。Grok Build 的命名 slash 启动当前固定使用默认 `agent_budget=128`，所以直接输入 `/gbs-max-collab` 会被生产 workflow 的精确预算门槛有意拒绝，也不会弹出参数选择器。

应用在 Max Collaboration 卡片中提供“在 Grok Build 中启动”复制区：选择 tier、填写任务目标，然后复制自然语言指令，例如：

```text
使用 Assurance 运行 gbs-max-collab，目标是：审查当前改动并运行最小相关测试
```

将该指令粘贴到 Grok Build 对话中，由 Grok 通过 workflow tool 传入 `args.objective`、`args.tier="assurance"` 和 `agent_budget=12`。其他 tier 会复制各自精确预算提示：Economy=1、Focused Evidence=2、Focused Build=11、Critical=13。包含主实现的 tier 由 workflow 顶层启动 10 个主实现 agent。不要直接使用 slash autocomplete 启动该命名 workflow。

每个角色使用其 role 文件中已选择并校验的推理强度；节省假设来自少调用、默认串行、复用结果包和限制重试，而不是强制固定档位。

点击“停用”只会切换 policy 的 enabled 状态，并保留 provider、四角色 Standard 锚点/速度档/effort、默认 tier、manifest 与磁盘文件；不会删除 `~/.grok/agents/gbs-*.md`、`~/.grok/roles/gbs-*.toml` 或 `~/.grok/workflows/gbs-max-collab.rhai`，也不改写当前 routing 或 `config.toml`。停用后的状态检查仍会报告保留 artifact 的缺失、hash 漂移、非 canonical 路径或符号链接。三个沿用的 Terra/Luna/Sol basename 只是稳定所有权标识，不代表角色必须绑定这些模型。浏览器和 web search 仍由普通路由 / Grok Build 能力独立配置。

## 7. 查看用量

用量页只读聚合 `~/.grok/logs/unified.jsonl`：

- 推理事件数；
- prompt 与 cached prompt tokens；
- completion 与 reasoning tokens；
- 缓存命中率；
- 按当前 session 元数据归属的模型。

该面板不会读取 transcript，也不会把订阅额度换算成虚构的美元成本。历史 session 摘要只能提供当前记录的模型，因此切换模型后的旧 turn 归属可能不精确。真实多任务试点不属于当前版本门槛；在没有独立、充分证据时不提供正式节省结论。

## 8. 使用订阅代理

1. 打开订阅代理管理；
2. 按受支持流程登录订阅账号；
3. 确认代理状态正常；
4. 在统一模型路由中选择代理提供的模型。

订阅代理账号、令牌和凭据是保留数据，不属于已移除能力的旧记录清理范围。

## 9. 编辑 `config.toml`

应用支持查看、校验和编辑 Grok CLI 的 `~/.grok/config.toml`。编辑前确认内容属于当前环境，保存后可在终端运行 `grok` 验证。

## 10. 使用 LAN 与 SSH

- **LAN**：仅在可信网络中开启，完成配对后再访问管理页；
- **SSH**：添加连接信息后执行受支持的远程文件操作；
- 不要在截图、日志或公开 Issue 中暴露 API Key、官方认证或订阅代理凭据。

## 11. 使用菜单栏与 Wails

- 通过 macOS 菜单栏快速打开应用、查看状态和执行常用切换；
- 使用 Wails 窗口完成 Profile、路由、订阅代理、配置、LAN 和 SSH 管理。

## 12. 产品范围

本教程第 3 至第 11 节覆盖当前完整产品范围。其他旧扩展已移除，不再提供现行操作入口。Max Collaboration 仅恢复配置控制面，不恢复 chat、session graph、transcript 或 `/api/agent/*`。


> Collaboration schema v5 defaults to `single_provider`. `federated` is an explicit-consent preview model with per-role provider and data-scope assignments; current active-provider/config serialization blocks safe multi-provider activation, so the Switch fails closed rather than merging credentials or pretending cross-provider routing works.
