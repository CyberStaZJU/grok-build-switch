# Plan.md — 工作计划

> 代理执行任务的计划与进度跟踪。任务完成后归档到「已完成」并更新 Status.md。
> 最后更新：2026-08-19

## 当前任务

（暂无活跃任务）

## 待办（按优先级）

1. **发布准备**
   - [ ] 获取本机有效 `Developer ID Application` 证书
   - [ ] 配置 `notarytool` profile
   - [ ] 发布前重跑全量 race 与 `check-work`
   - [ ] 产出正式签名 + 公证的 DMG 与 sha256
2. **技术债**
   - [ ] 拆分 `server.go`（按配置、模型探测等资源）
   - [ ] 加强 TOML 直编与统一路由的一致性校验
3. **验证补强**
   - [ ] 五条 tier 在真实 host 下的 path-specific 验证（当前被 canned host budget 问题阻塞）
   - [ ] Focused Evidence / Focused Build / Assurance / Critical 的真实试点（如需另立研究项目）

## 已完成

- [x] 2026-08-19 审查、验证并发布当前 `main` 到 GitHub；待提交文件的密钥/私有路径扫描无发现，前端 Node、全量 Go、vet、受影响包 race 与 Wails-tag 测试通过，构建产物和本机运行数据未进入 Git。
- [x] 2026-08-19 修复 CodeBuddy 托管 Profile 的供应商后缀别名污染与默认模型子集缺失；本机 Profile 经管理 API 修复，Flash/Pro 代理调用恢复。实测两者网关上限为 1,048,576 tokens，配置窗口按用户决定设为 1,000,000；约 1,039,948 prompt tokens 的开头与中部标记均能准确召回。
- [x] 2026-08-18 建立根目录 AGENTS.md / Status.md / Plan.md，并与 docs/ 现有文档建立引用关系。

## 使用约定

- 开始新任务时在「当前任务」写下目标、步骤清单与验收标准。
- 每完成一步勾选并记录阻塞；遇到与 Status.md 边界冲突的事项先停下来报告。
- 任务结束：勾选全部步骤 → 移入「已完成」→ 更新 Status.md。
