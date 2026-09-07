# Plan.md — 工作计划

> 代理执行任务的计划与进度跟踪。任务完成后归档到「已完成」并更新 Status.md。
> 最后更新：2026-09-07

## 当前任务

暂无正在进行的任务。当前阶段需求均已完成并通过验证。

## 待办（按优先级）

1. **发布准备**
   - [ ] 获取本机有效 `Developer ID Application` 证书
   - [ ] 配置 `notarytool` profile
   - [ ] 产出正式签名 + 公证的 DMG 与 sha256
2. **技术债**
   - [ ] 拆分 `server.go`（按配置、模型探测等资源）
   - [ ] 加强 TOML 直编与统一路由的一致性校验

## 已完成

- [x] 2026-09-07 升级 CLIProxyAPI 到 7.2.152，支持 gpt-6-astra 与 gemini-3.7/3.8-flash-high。
- [x] 2026-09-07 实现 Codex 与 Google 只读额度读取、同厂商聚合端点与桌面概览展示。
- [x] 2026-09-07 更新 Codex (372k)、Astra (272k)、Gemini (1048k) 上下文长度建议值，并修复前端表单步长为 1 token。
- [x] 2026-09-07 全量 Go、vet、并发 race、28 项前端单元测试及隔离浏览器端到端验收通过。
- [x] 2026-09-07 构建 0.9.5 build 19 覆盖安装至 `/Applications/Grok Build Switch.app`，重启并完成生产额度实测与 Astra 真实推理验证（HTTP 200 Pong!）。
- [x] 2026-09-05 回退为 Grok 专用，保留 GPT 代理，构建安装 0.9.4 build 18。
- [x] 2026-09-02 修复 CodeBuddy 工具调用 ID 协议兼容，构建安装 0.9.3 build 12。
- [x] 2026-08-30 增加 Sol Ultra 档位支持与 CodeBuddy 模型暴露多选，构建安装 0.9.2 build 11。
