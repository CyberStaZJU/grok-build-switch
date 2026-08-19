# Status.md — 项目当前状态（代理工作摘要）

> 权威细节见 [docs/status.md](docs/status.md)；本文件为代理快速上下文，保持精简并及时更新。
> 最后更新：2026-08-19

## 当前可用能力

- 官方 Grok CLI 登录与模型路由；普通 Profile 管理（供应商/URL/Key/格式/模型/上下文窗口）。
- 单一启用供应商路由（routing.json schema v2），路由切换事务化、失败回滚。
- 订阅代理：内嵌 CLIProxyAPI；ownership ledger + marker + 跨进程锁 + recovery journal；未知并发状态 fail closed。
- Max Collaboration（collaboration.json schema v5，四角色 Standard/Fast + effort）；apply 写入顺序 artifacts → config → routing → policy，后段失败补偿回滚。
- 用量观察（只读聚合 token 与缓存命中率，不推算成本）。
- 配置编辑、菜单栏/Wails 壳、LAN 配对、SSH 远程管理。

## 进行中 / 阻塞

- **发布阻塞**：本机缺少有效 `Developer ID Application` 身份与 notarytool profile；当前只能产出 ad-hoc 本地候选，不能声称已签名/已公证。
- 发布前重跑：最新全量 race 与独立 `check-work`。
- 五条 tier 的生产 renderer 脚本在 canned host 下未采用精确 budget，不能记为 path-specific PASS；仅完成一次 Economy 最小只读 live smoke。

## 技术债（要点）

- `server.go` 偏大，可继续按资源拆分。
- TOML 直编入口需持续加强与统一路由的一致性校验。
- `SupportsBackendSearch` 依赖 Profile 声明，无通用自动探测。
- 用量 per-turn 模型归属是近似值（按 session summary 归属），不可作精确计费证据。
- CLIProxy 上游管理端点无 ETag/CAS，无法与不合作外部写入者形成真正原子事务。

## 最近变更

- 2026-08-19：发布前完成待提交代码与轻量文档的隐私审计；常见密钥格式与私有 home 路径扫描无发现，`dist/`、`vendor/`、本机配置和运行凭据保持忽略、不进入 Git。前端 Node 测试、`go test ./...`、`go vet ./...`、受影响包 race 与 Wails-tag 测试均通过。
- 2026-08-19：修复 CodeBuddy Profile 将 `@CodeBuddy / WorkBuddy` 显示后缀误作上游模型 ID、以及选择未启用默认模型时静默回退的问题；本机 Profile 已通过管理 API 规范化为 `hy3`、`deepseek-v4-flash`、`deepseek-v4-pro`。
- 2026-08-19：实测 CodeBuddy 的 `deepseek-v4-flash` 与 `deepseek-v4-pro` 网关上限为 1,048,576 tokens；1,048,538 prompt tokens 可接受，1,048,548 prompt tokens 被拒绝。配置窗口按用户决定设为 1,000,000，为系统提示、工具调用和输出保留余量；两者在约 1,039,948 prompt tokens 均可准确召回开头与中部标记。
- 2026-08-18：建立根目录 AGENTS.md / Status.md / Plan.md 代理协作文件。
- 2026-08-18（docs/status.md）：官方 default 保留全目录；详见权威状态文档。
