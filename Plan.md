# Plan.md — 工作计划

> 代理执行任务的计划与进度跟踪。任务完成后归档到「已完成」并更新 Status.md；历史细节由 [docs/status.md](docs/status.md) 承载。
> 最后更新：2026-09-23

## 待办（按优先级）

1. **发布准备**
   - [ ] 获取本机有效 `Developer ID Application` 证书
   - [ ] 配置 `notarytool` profile
   - [ ] 产出正式签名 + 公证的 DMG 与 sha256
2. **技术债**
   - [ ] 拆分 `server.go`（按配置、模型探测等资源）
   - [ ] 加强 TOML 直编与统一路由的一致性校验
   - [ ] 清理 `cliproxy/config.yaml` 中 3 条先于 0.9.15 的历史 priority 规则副本（当前受管规则正确、两次 reconcile 后 payload 不变，但历史副本不会被 `mergeManagedFastRule` 清理）
   - [ ] 为能力探测评估缓存失效策略：目前模型一旦 `efforts_known` 便不再重测，上游改变档位或新增 Fast 支持不会被发现
3. **可选（等待用户决定）**
   - [ ] 跟进 `gpt-6-sol` 的账号/上游访问权限（当前 `model_not_found`；7.3.15 目录会短暂列出它，需注意不要在无访问权限时误授权）
   - [ ] 评估是否让模型目录按上游 `/v1/models` 实时校验，避免第三方上游下线模型后再次留下陈旧选项
   - [ ] 评估在多供应商混合目录下是否也要固定其他依赖单一模型的辅助调用（当前只固定 `session_summary`）

## 已完成（近期）

- [x] 2026-09-23 升级内嵌 CLIProxyAPI 到 7.3.15，构建安装 `0.9.17 (build 33)`；期间修正能力探测缺陷（须同时验证真实可达性）并把能力记录升到 v2；旧 bundle 与旧 CLIProxyAPI 副本无备份删除。
- [x] 2026-09-23 订阅代理模型能力自动探测：目录更新时为 Codex 新模型探测可接受推理档位，并据此自动生成 Standard/Fast 配对与档位声明；构建安装 `0.9.15 (build 31)`，生产实测 7 条 Fast 别名与真实请求通过，旧 bundle 无备份删除。
- [x] 2026-09-20 升级内嵌 CLIProxyAPI 到 `7.3.9`，重新构建并安装 Grok Build Switch `0.9.14 (build 30)`；保留配置、账号认证和订阅凭据，生产重启后健康检查通过；确认新实例稳定后删除旧回退 App。
- [x] 2026-09-20 重新构建并安装 `0.9.13 (build 29)`：隔离 HOME 通过全量构建门禁，使用外部原子安装锁、`.incoming` 预校验、旧进程等待退出、健康检查后卸载旧包，确认新实例健康且无并发残留。
- [x] 2026-09-20 修复 `PUT /api/profiles/{id}` 丢弃 `upstream_base_url`（编辑页保存会让流式保护失去上游 → 全部请求 503）；补经 handler 驱动的回归测试，生产已恢复该字段并验证端到端。
- [x] 2026-09-20 定位并处理 API 池 `gpt-6-astra` 404：原始上游不提供 Astra，已从 API 池模型目录移除；订阅代理别名保留并记录实际响应模型。
- [x] 2026-09-20 删除旧 `0.9.12` 及更早版本的 App、DMG 和 `.sha256` 安装包；保留配置快照、日志和运行数据。
- [x] 2026-09-19 流式保护：诊断 API 池 `keepalive` 序列化错误（网关自定义 SSE 帧撞上 Grok CLI 封闭事件枚举）；实现 `internal/streamguard` 与 loopback-only `/stream-guard/v1/*`；真实帧回放加固（含 `response.failed` 缺 `output` 的回填）；顺带把 CodeBuddy `finish_reason: "error"` 归一为 `null`；修复开关 `routingMu` 重入死锁；构建安装 `0.9.12 (build 28)` 并为「API 池」启用。
- [x] 2026-09-18 接入 API 池供应商：启用自定义供应商时一并写入 `[models].session_summary`，消除内置 `grok-4.6` 的 404 标题请求；连接测试不再把网关 HTML 200 当成功。安装 `0.9.11 (build 27)`。
- [x] 2026-09-15 修复 CodeBuddy 托管 Profile 编辑页必然 409（未设置档位与 `none` 视为同值）；模型卡片支持按模型声明推理强度档位，DeepSeek `deepseek-flash` 声明 `low/medium/high/xhigh/max`。安装 `0.9.10 (build 26)`、`0.9.9 (build 25)`。
