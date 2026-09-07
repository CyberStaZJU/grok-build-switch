# Status.md — 项目当前状态（代理工作摘要）

> 权威细节见 [docs/status.md](docs/status.md)；本文件为代理快速上下文，保持精简并及时更新。
> 最后更新：2026-09-07

## 生产安装与更新（2026-09-07）

按用户指令完成 `/Applications/Grok Build Switch.app` 覆盖安装并重启服务：
- 安装版本 `0.9.5 (build 19)`，内置 `CLIProxyAPI 7.2.152`（ad-hoc 签名，未公证）；签名严格校验通过，进程从安装目录运行并监听 `127.0.0.1:17878`。
- 安装前配置备份于 `~/.grok/build-state/grok-build-switch-preinstall-20260907/`。
- 生产界面实测：订阅代理页成功展示「额度概览」，读取 2 个 Codex 与 2 个 Google 账号状态。Codex 观测到 primary 70%、各模型独立池及 0 credits；Google 明确标注 unknown 并给出原因；刷新时间与失败清空正常。
- 真实连通：使用 `subscription/codex/gpt-6-astra` 通过应用代理发起最小真实推理，上游返回 HTTP 200 及 `Pong!`。
- 上下文默认值：GPT-5.6 建议值升至 372000（Team/Plus 上限），Astra 272000，Gemini 3.6/3.7/3.8 Flash High 为 1048576；表单步长修正为 1 token。

## 当前可用能力

- 官方 Grok CLI 登录与模型路由；普通 Profile 管理（供应商/URL/Key/格式/模型/上下文窗口）。
- 单一启用供应商路由（routing.json schema v2），路由切换事务化、失败回滚。
- 订阅代理：内嵌 CLIProxyAPI 7.2.152，支持 Codex/Google 被动额度观测与同厂商聚合展示。
- Max Collaboration（collaboration.json schema v5，四角色 Standard/Fast + effort）。
- 用量观察（只读聚合 token 与缓存命中率，不推算成本）。
- 配置编辑、菜单栏/Wails 壳、LAN 配对、SSH 远程管理。

## 进行中 / 阻塞

- **发布阻塞**：本机缺少有效 `Developer ID Application` 身份与 notarytool profile；当前只能产出 ad-hoc 本地候选，不能声称已签名/已公证。

## 技术债（要点）

- `server.go` 偏大，可继续按资源拆分。
- TOML 直编入口需持续加强与统一路由的一致性校验。
- `SupportsBackendSearch` 依赖 Profile 声明，无通用自动探测。
- 用量 per-turn 模型归属是近似值（按 session summary 归属），不可作精确计费证据。
- CLIProxy 上游管理端点无 ETag/CAS，无法与外部写入者形成真正原子事务。

## 最近变更

- 2026-09-07：升级内置 CLIProxyAPI 到 7.2.152；支持 gpt-6-astra 与 gemini-3.7/3.8 模型；实现 Codex 与 Google 只读额度 API、同厂商聚合与桌面展示；更新模型上下文建议值并修复步长；构建并覆盖安装 0.9.5 build 19。
- 2026-09-05：回退为 Grok 专用，保留 GPT 反代，安装 0.9.4 build 18。
- 2026-09-02：修复 CodeBuddy 工具调用 ID 兼容，安装 0.9.3 build 12。
- 2026-08-30：接入 Sol Standard/Fast `ultra` 档位；增加 CodeBuddy 暴露模型多选；安装 0.9.2 build 11。
