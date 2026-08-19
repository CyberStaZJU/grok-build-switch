# AGENTS.md — 给编码代理的协作约定

> 本文件是代理在本仓库工作时的常驻约定。详细维护者说明见 [docs/agent.md](docs/agent.md)。
> 最后更新：2026-08-18

## 项目速览

- **Grok Build Switch**：macOS（15+, Apple Silicon）菜单栏/Wails 工具，管理 Grok CLI 的 `~/.grok/config.toml`、供应商 Profile、统一模型路由、订阅代理（内嵌 CLIProxyAPI）、用量观察、LAN/SSH 远程访问。
- 语言：Go（`go.mod` 锁定）+ 原生 JS 前端（`ui/`）；Wails 桌面壳。
- 构建：`./build-macos.sh`（仅 arm64，产物在 `dist/macos/`）。

## 工作习惯（务必遵守）

1. **开始任务前**：先读 [Status.md](Status.md) 了解当前状态与边界；有明确任务时读 [Plan.md](Plan.md) 了解计划。
2. **任务进行中**：在 Plan.md 勾选已完成步骤、追加阻塞与发现。
3. **任务结束时**：更新 Status.md（状态变化、新增技术债）、Plan.md（进度/已完成任务归档），并注明日期。
4. 日期格式 `YYYY-MM-DD`；内容务实，不写无证据的结论。

## 硬性边界

- 不删除/改写 Grok CLI 官方认证数据；不删除订阅代理凭据。
- 未知归属的 DataDir 记录：保留并报告，不扩大清理。
- 订阅代理并发状态未知时 **fail closed**，不覆盖无关用户配置。
- 路由修改走既定事务顺序（校验 → 投影 → 原子写 config.toml → 持久化 routing.json → 失败回滚）。
- 管理端点 loopback-only、strict JSON、CSRF 保护；不得放宽。
- 不把 ad-hoc 签名描述为正式 Developer ID 签名/公证。

## 验证

- 测试：`go test ./...`（含 `ui/app_behavior_test.js` 对应的前端测试与 `*_test.go`）；race：`go test -race` 关键包。
- 注意仓库根存在 `.verification_forbidden` 标记——涉及受限验证动作前先确认范围。
- 构建/发布相关阻塞（签名、公证）记录在 Status.md 第 3 节。

## 文档地图

- 产品说明：`docs/product.md`；使用教程：`docs/usage.md`
- 当前状态（权威细节）：`docs/status.md`；维护者说明：`docs/agent.md`
