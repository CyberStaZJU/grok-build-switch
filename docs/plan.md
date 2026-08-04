# Grok Build Switch — Plan 文档

> 近期收尾、中期方向与风险。最后更新：2026-08-04。

---

## 0. 本期产品收敛

当前产品范围已收敛为：Grok CLI 官方登录与路由、普通 Profile、统一模型路由、Max Collaboration 配置控制面与用量观察、订阅代理、`config.toml` 编辑、LAN、SSH、macOS 菜单栏和 Wails 桌面窗口。

核心范围之外的旧扩展已移除。当前文档、界面和运行入口只展示上述保留能力，不再保留被删功能、接口或数据目录的现行清单。

---

## 1. 近期验证与收尾

### 1.1 保留功能回归

- 验证 Grok CLI 官方登录由官方流程完成，登录后官方路由可正常应用。
- 验证普通 Profile 的创建、编辑、删除和模型选择；协议格式由显式 `upstream_format` 选择，类型模板不再提供。
- 验证 default、web_search、explore、plan 的统一路由事务和失败回滚。
- 验证 Max Collaboration schema v4 的同一可信 Codex provider 四角色 Standard 锚点/Standard-Fast 速度档/effort 独立校验、严格 v1/v2/v3→v4 迁移（旧 policy consent 为空、data scope 固定映射）、Fast fail-closed、preview/fingerprint、九个 canonical artifact（四个 agent definition、四个 role、一个 workflow）的清单/类型/hash/写前竞态保护、跨文件回滚、只对齐 routing default 的保存语义、policy-only disable 和 1/2/3/4 workflow 精确预算。
- 验证 CLIProxy 完整 YAML 所有权合并、canonical ledger/认证 marker、二次 GET rebase、write-ahead recovery journal、跨进程 operation lock、受管 alias 出现/消失的稳定目录收敛、只读状态与显式 reconciliation 分离、精确 Fast `service_tier: priority` 注入、Standard 无 priority、严格一次性旧 ownership 迁移和失败不覆盖用户配置。
- 验证用量面板聚合 completion/reasoning token，且缺失字段和 reasoning-only 事件处理正确。
- 验证订阅代理生命周期、账号状态与代理路由，不改变其凭据。
- 验证 `config.toml` 查看、校验和编辑流程。
- 验证 LAN 配对、CSRF、SSH、菜单栏与 Wails 入口。
- 验证当前 UI 和当前产品文档不再展示已移除能力。

### 1.2 DataDir 清理

2026-07-31 已完成旧 `backups/` 目录清理并核验保留数据。其余历史记录继续按明确归属逐项审计。

用户已授权清理已移除能力留下的应用自有旧记录。执行时必须：

1. 只匹配能够确认属于已移除能力的记录；
2. 保留 Grok CLI 官方认证；
3. 保留订阅代理账号和凭据；
4. 保留普通 Profile、统一路由、LAN、SSH 与应用设置；
5. 对未知记录停止自动处理并报告。

### 1.3 安全与一致性收尾

- LAN 公开响应与内部含密钥数据结构分离，敏感路由保持 loopback-only。
- GET 状态/路由保持只读，设置、Profile 与路由修改具备失败回滚。
- 管理型 JSON 请求统一限制大小、拒绝未知字段和尾随文档，并保持服务端超时与并发状态安全。
- Anthropic 官方 API 直连不属于支持范围；第三方 `messages` 兼容网关仍可作为自定义 Profile 使用。
- 模型目录只优先采用真实 ID，探测需明确确认并限制请求数量。

### 1.4 发布前检查

- 运行当前项目规定的静态检查和 Go 测试。
- 构建默认入口与 `wailsgui` 入口。
- 已对全新应用包执行隔离启动和四个只读核心端点 smoke，并核验 bundle、ad-hoc 签名、arm64、macOS 15.0 minimum target 与 DMG SHA-256；菜单栏/窗口人工交互仍留给签名候选阶段。
- 生产 renderer 的五条 tier 已有单元级路径/预算/角色覆盖。top-level `validate_only` 当前受 canned-host budget 不采用请求预算所阻塞，脚本会按设计 fail closed；在工具契约修复前如实记录为“编译通过、路径证明未完成”，不得弱化精确预算门槛。已完成一次精确 budget=1 的 Economy 最小只读 live smoke；其他 tier 和 Fast 仍需单独授权。
- Max Collaboration UI、教程和 workflow metadata 必须持续说明：named slash launch 使用默认 budget 128，不提供参数选择；用户应复制带 objective/tier 的自然语言指令，让 Grok 通过 workflow tool 传入精确预算。
- 发布前重跑 `go test -race ./...` 和独立 `check-work`，修复所有阻断项。
- Developer ID 签名与 Apple 公证必须使用真实本机凭据；当前缺少有效 identity/profile 时暂停正式资产发布，不得以 ad-hoc 候选替代。
- 检查发布说明只描述当前能力；历史博客保持原样。

---

## 2. 中期方向

### 2.1 路由一致性

- schema v2 已落地唯一启用供应商、每供应商记忆策略、稳定 provider-local 引用和确定性 v1 迁移。
- 继续收敛 `config.toml` 与 `routing.json` 的一致性语义，特别是手工编辑后的别名兼容与漂移提示。
- 区分有效的 Grok CLI 外部模型切换与真正的配置漂移。
- 持续扩展官方、自定义和订阅代理的统一激活/策略事务测试；保持无 chat/session graph。

### 2.2 Collaboration 试点与用量归属

- 在 workflow canned host 能正确采用请求 budget 后，对生产 renderer 的 Economy、Focused Evidence、Focused Build、Assurance、Critical 五条路径分别运行 `validate_only`；该检查只覆盖编译和 canned-host 所选路径，不等于 live smoke。
- Economy 精确 budget=1 的最小只读 live smoke 已完成。经用户另行授权后，再逐层运行其余 tier，确认四个角色实际采用所选具体 Standard/Fast route 与推理档位、精确预算生效，且 Fast 不静默 fallback。
- 真实多任务成本/质量/返工率对照不作为当前版本的完成或发布门槛；若未来资源允许，可作为单独、可中止的研究项目进行。没有这类证据时，产品只证明配置、路由、预算和事务行为，不宣称实际节省比例、质量优势或固定 Fast 倍率。
- 若日志契约允许，在不读取 transcript 的前提下增加可靠的 per-turn/per-agent 模型归属；在此之前明确展示 session-level 归属限制。
- 只有用户提供可靠价格/配额权重时才考虑可配置的相对成本单位，不硬编码美元价格。
- 项目级 artifact export、受管文件显式删除和 policy 迁移属于后续单独产品决策；默认停用仍只保留文件。

### 2.3 模型能力探测

- 以可执行能力为准展示搜索、协议和上下文支持。
- 不基于模型名称猜测工具能力。
- 普通 Profile UI 保持聚焦，只展示当前路由所需的基础模型选择。

### 2.4 服务端与桌面入口

- 将配置读写、模型列表和能力探测从大型服务文件中继续拆分。
- 增加请求大小、未知字段、鉴权和 CSRF 覆盖。
- 为菜单栏、Wails 和浏览器管理页建立一致的核心功能测试矩阵。

---

## 3. 长期方向

- 发布自动化、签名、公证和 GitHub Release 资产验证。
- 更清晰的版本变更与迁移说明。
- 对保留的敏感运行配置进行权限和脱敏审计。
- 保持产品围绕 Grok CLI 配置与路由管理，不在无新决策时扩回已移除范围。

---

## 4. 风险

| 风险 | 缓解措施 |
|:---|:---|
| 当前文档或 UI 重新出现已移除能力 | 发布前执行产品边界关键词和界面入口审查 |
| DataDir 清理误伤保留凭据 | 使用明确归属 allowlist；官方认证与订阅代理凭据设为禁止删除 |
| 路由与 `config.toml` 漂移 | 保持事务写入、校验和回滚测试 |
| stale preview 覆盖用户编辑 | fingerprint + 精确 canonical manifest + hash/文件类型 + 每个受管文件写前复核；冲突 fail closed |
| Fast 被错误推断或静默降级 | 只信任 exact registry + 显式同供应商 Standard anchor；缺失/歧义/伪造直接失败，不按后缀推断 |
| Fast credit 消耗被低估 | UI/preview 明确警告；速度与 effort 分离；不声称固定倍率，live 试点需单独授权 |
| 默认 workflow budget 误触发大量 agent | 生成脚本要求 tier 对应精确 1/2/3/4；拒绝默认 128 |
| token 面板被误解为美元节省 | 只报告原始 token/turn，并明确真实成本与质量需试点 |
| 非原生搜索路由被误认为可用 | 只暴露实际可执行能力 |
| 多入口行为不一致 | 对菜单栏、Wails 和浏览器管理页执行同一 smoke 矩阵 |


> Collaboration schema v4 defaults to `single_provider`. `federated` is an explicit-consent preview model with per-role provider and data-scope assignments; current active-provider/config serialization blocks safe multi-provider activation, so the Switch fails closed rather than merging credentials or pretending cross-provider routing works.
