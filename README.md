# Grok Build Switch

Grok Build Switch 是面向 macOS 的本地菜单栏/桌面工具，用于管理 Grok CLI 的 `~/.grok/config.toml`、供应商 Profile、统一模型路由，以及可选的 Grok Build Max Collaboration 配置预设。

> 本仓库基于 [1parado/grok-build-switch](https://github.com/1parado/grok-build-switch) 开发，并集成 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 提供订阅账号代理能力。感谢原项目及相关开源项目的工作。

## 当前产品能力

- **官方 Grok CLI**：使用 Grok CLI 官方登录流程，并在登录后应用官方模型路由
- **普通 Profile**：管理供应商名称、Base URL、API Key、上游格式和模型
- **单一启用供应商路由**：官方或自定义供应商互斥启用；每个供应商记忆自己的 `default`、`web_search`、`subagents.explore` 与 `subagents.plan`，自定义切换保留组合模型目录以兼容旧会话别名
- **Max Collaboration 控制面**：为主协调、任务拆解、主实现、困难实现 / 复核四个角色分别选择同一可信 Codex 订阅供应商内的 Standard 模型锚点、独立 Standard/Fast 速度档与推理强度，预览后生成用户级 Grok Build role/workflow，并以串行 1/2/3/4 精确预算门控调用
- **用量观察**：只读聚合近期 prompt、cached prompt、completion 与 reasoning token 及缓存命中率；不把订阅用量虚构为美元成本
- **订阅代理**：通过内嵌 CLIProxyAPI 接入和管理受支持的第三方订阅；Switch 以 canonical ownership ledger、认证 marker、跨进程锁和 recovery journal 合并完整 YAML，仅对 exact-registry Fast aliases 注入 priority；未知并发状态 fail closed，不覆盖无关用户配置
- **配置编辑**：查看、校验并编辑 `~/.grok/config.toml`
- **远程访问**：支持 LAN 配对访问和 SSH 连接管理
- **桌面入口**：提供 macOS 菜单栏与 Wails 桌面窗口

## 产品边界

上述清单是当前产品的完整功能范围；不在其中的旧扩展已从当前产品移除，不再作为可用功能、接口或数据目录出现在现行说明中。

用户已授权应用清理这些已移除能力遗留在自身 DataDir 中的旧记录。该授权不包含 Grok CLI 官方认证数据，也不包含订阅代理保存的账号或凭据；应用不得将两者作为旧记录删除。

## 系统要求

| 项目 | 说明 |
|------|------|
| 系统 | **macOS 15+**（仅 Apple Silicon / arm64） |
| 运行 | 双击 `Grok Build Switch.app` 即可 |
| 可选 | 本机已安装 [Grok CLI](https://x.ai)，配置目录默认为 `~/.grok` |

## 安装与使用

### 当前方式：从源码构建

当前仓库尚无可下载的 Release 资产，现阶段请从源码构建。后续发布后，可从 [Releases](../../releases) 页面下载 `Grok-Build-Switch-*.dmg`，拖入 Applications 文件夹运行。

```bash
./build-macos.sh
```

构建脚本仅支持在 Apple Silicon Mac 上生成 `dist/macos/Grok Build Switch.app`、arm64 DMG 和对应的 `.sha256`；应用与内嵌 CLIProxyAPI 最低支持 macOS 15。当前不提供 Intel 构建。默认 bundle marketing version 为 `0.0.0`、build version 为 `1`；发布构建可通过 `MARKETING_VERSION=1.2.3 BUILD_VERSION=42` 覆盖，两者必须符合 Apple 的纯数字点分格式。兼容旧调用时，`VERSION` 仍可作为 marketing version 输入。

Developer ID 签名：

```bash
APPLE_SIGNING_IDENTITY="Developer ID Application: Example (TEAMID)" \
  ./build-macos.sh --require-signature
```

## 文档

- [产品说明](docs/product.md)
- [使用教程](docs/usage.md)
- [当前状态](docs/status.md)
- [维护者说明](docs/agent.md)

在线文档站：[https://1parado.github.io/grok-build-switch/](https://1parado.github.io/grok-build-switch/)

## 数据与安全

- `~/.grok/config.toml` 是 Grok CLI 当前生效配置。
- 应用 DataDir 保存普通 Profile、统一路由、`collaboration.json`、应用设置、LAN/SSH 配置和订阅代理运行数据。
- 启用 Max Collaboration 时，Switch 经 preview/fingerprint/确认后管理 `~/.grok/agents/gbs-*.md`、`~/.grok/roles/gbs-*.toml` 和 `~/.grok/workflows/gbs-max-collab.rhai`；agent definition 注册可供 workflow 使用的 `agent_type`，role TOML 提供模型、推理强度和默认 capability。manifest 必须恰好覆盖九个 canonical 路径，同名未知文件、缺失/漂移文件、符号链接或其他非普通文件都会 fail closed。
- 停用预设只更新 collaboration policy：不删除 agent/role/workflow，也不改写当前 routing 或 `config.toml`；状态页仍会检查保留 artifact 的 manifest 与 hash 漂移。
- Switch 是配置/路由控制面，不启动 agent，不保存消息、transcript 或 session graph；workflow 的真实运行和 `agent_budget` 由 Grok Build 负责。
- Profile 中的 API Key 以及订阅代理账号凭据属于敏感数据，不应提交到 Git。
- Grok CLI 官方认证由官方登录流程管理；清理应用旧记录时不得删除或改写该认证。
- 订阅代理凭据属于保留功能的数据；清理应用旧记录时不得删除。

## Max Collaboration 使用边界

- Collaboration schema v4 为四个角色各自保存 **Standard route anchor**、显式 `speed_tier`（`standard` / `fast`）与 `reasoning_effort`；同一模型可以复用，速度档与推理强度彼此独立。
- Standard 沿用现有逻辑身份且不注入 priority。Fast 仅在 exact-registry 可信 Terra/Sol/Luna Standard↔Fast 关系完整时解析为 `subscription/codex/<model>-fast`，由 CLIProxy 对该精确别名注入 `service_tier: priority`；缺失、歧义或伪造关系直接失败，不按名称/后缀推断，也不静默回退 Standard。
- 推理强度在解析后的具体 Standard/Fast route 上校验，必须由 `declared` 或 `probe` 明确支持。Fast 通常更快但会消耗更多订阅 credits；项目不声称固定倍率。
- v1/v2 policy 读取时只在内存迁移为 Standard，且不改写旧文件；旧 policy 若把具体 `-fast` ID 当作 model，因无法安全推断锚点而 fail closed，需用户显式修复，避免迁移意外提高 credit 档。
- 应用只把普通路由的 `default` 具体模型和默认推理强度对齐到主协调；`web_search`、`explore` 与 `plan` 仍由普通路由管理，浏览器/搜索能力与 Collaboration 正交。
- 独立 workflow 层级为 Economy=1、Focused Evidence/Build=2、Assurance=3、Critical=4。Grok Build 的 named slash launch 当前固定使用默认 budget 128 且不提供参数选择，因此不要直接输入 `/gbs-max-collab`。应用会按所选 tier 和任务目标生成可复制的自然语言启动指令，由 Grok 通过 workflow tool 传入完全匹配的 `agent_budget`。
- `Adaptive` 是 Economy-first 配置提示，不会自动运行 workflow；`Critical` 必须显式选择。
- 当前实现和本地测试不等于真实订阅成本结论。只有在经授权的 live 试点证明质量不下降且总用量更低后，才能宣称实际节省。

## License

[MIT](./LICENSE)


> Collaboration schema v4 defaults to `single_provider`. `federated` is an explicit-consent preview model with per-role provider and data-scope assignments; current active-provider/config serialization blocks safe multi-provider activation, so the Switch fails closed rather than merging credentials or pretending cross-provider routing works.
