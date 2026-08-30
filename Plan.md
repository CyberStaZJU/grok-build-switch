# Plan.md — 工作计划

> 代理执行任务的计划与进度跟踪。任务完成后归档到「已完成」并更新 Status.md。
> 最后更新：2026-08-30

## 当前任务

暂无。

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

- [x] 2026-08-30 按用户决定暂不接入 `ultra`、只接入到 `max`：供应商编辑菜单与可信 Codex Standard/Fast 能力均移除 `ultra`，陈旧声明会被过滤且不能绕过服务端校验；Switch 内部 `reasoning_efforts_source` 不再投影到 Grok `config.toml`。修复托管 Profile effort-only 保存因提前补齐上下文窗口而误报 409，以及规范化切片复用导致原 Profile 元数据被修改的问题。前端 21 项、全量 Go、vet、相关包 race、Wails-tag 测试通过；Standard/Fast 的 `max` 均实测返回 `OK`，`ultra` 被 Grok Build 1.0.13 拒绝。已构建并安装 `0.9.2 (build 11)` ad-hoc 候选，主程序 SHA-256 `65e5023dd283b6390a19534c03f20f05cc034fded41a3607219f6f48f09eb918`，与仓库构建副本逐字节一致；桌面与 `390×844` 移动端首页、Codex 编辑、订阅代理和 CodeBuddy 页面通过，托管供应商保存 `max` 成功且无横向溢出。验证后 Profile 恢复 Standard+Medium，全局路由恢复 Fast+High，`config.toml`/routing 一致且不含 `ultra` 或 `reasoning_efforts_source`。浏览器录制位于 `~/.config/browser-harness/agent-workspace/recordings/gbs-max-only-final`。
- [x] 2026-08-30 按用户要求只保留一个最新版 App：删除仓库中被 Git 忽略的 `dist/macos/Grok Build Switch.app` 构建副本，以及 `~/.grok/build-state` 下三个已核验为 0.8.0/0.9.0 的旧 App bundle；各父目录中的配置快照、清单和其他证据文件未删除。Spotlight 全局应用索引现只返回 `/Applications/Grok Build Switch.app`；该安装仍为 `0.9.1 (build 10)`，主程序哈希未变并正常监听 `127.0.0.1:17878`。保留的 0.9.1 DMG 与 `.sha256` 是安装介质，不是第二个已安装 App，校验仍通过。
- [x] 2026-08-30 为 CodeBuddy 增加独立的 Grok Build 暴露模型多选：默认模型必须属于所选子集，空子集被前后端拒绝，保存时只更新 CodeBuddy Profile 并通过统一路由保留其他供应商。前端 21 项、全量 Go、vet、相关包 race 与 Wails-tag 测试通过；仓库外隔离实例验证只暴露 `hy4-preview`、另一供应商的 `other-model` 保持存在、代理 `/v1/models` 只返回 `hy4-preview`，桌面流程与 `390×844` 移动布局通过。隔离状态保留在 `~/.grok/build-state/grok-build-switch-codebuddy-subset-20260830`，待获得清理授权。
- [x] 2026-08-30 以 `MARKETING_VERSION=0.9.1 BUILD_VERSION=10 ./build-macos.sh` 构建并安装 CodeBuddy 子集版本。旧 `/Applications/Grok Build Switch.app` 已停止并删除，新 App 从校验后的 DMG 安装；运行配置、凭据和账号数据保留，安装前配置快照位于 `~/.grok/build-state/grok-build-switch-preinstall-config-20260830T205215`。当前安装为 `0.9.1 (build 10)`，arm64 主程序 SHA-256 `be9d6590c1718fc4d37e23f9bf54f1ebdee28b4214500b5125f29b5cd1bd9a6c`，与 DMG 内主程序逐字节一致；App 为 ad-hoc 签名且未公证，进程从 `/Applications/Grok Build Switch.app` 运行并监听 `127.0.0.1:17878`。真实页面验证 CodeBuddy 当前只暴露 `hy4-preview` 与 `hy4-preview-x`、默认 `hy4-preview-x`，其他供应商模型仍在 Grok `/m`，桌面与 `390×844` 移动布局通过。Finder 单独显示的时间不是版本依据；实际 bundle/binary 于 20:51–20:52 创建并在 20:54 安装。
- [x] 2026-08-30 将 CodeBuddy 本机目录同步、Codex Sol Ultra、托管 Profile/路由边界加固、测试和文档提交并推送到 `https://github.com/CyberStaZJU/grok-build-switch` `main`。功能提交 `ef5d4522e75e322f08cc9a0e12cd217526050234` 已由 `git fetch`、`origin/main` 和 `git ls-remote` 三方核验一致；最终独立审查无阻断项，工作区除忽略的 `.DS_Store`、`dist/`、`vendor/` 外保持干净。
- [x] 2026-08-30 将重建的 `0.9.0 (build 9)` DMG 安装到 `/Applications/Grok Build Switch.app` 并重启。旧 App 已移动到 `~/.grok/build-state/grok-build-switch-installed-backup-20260830T043532Z`；另在 Codex 托管 Profile 显式同步前保存 `~/.grok/build-state/grok-build-switch-live-ultra-sync-backup-20260830T044202Z`。已核验版本、进程、端口、主程序哈希、ad-hoc 签名、API 与路由一致性；真实桌面页面保存 Standard+Ultra、受保护路由切换 Fast+Ultra、移动端 `390×844` 首页/订阅/编辑页无横向溢出。最终默认恢复为 Sol Standard+Medium，Standard/Fast 均保留 `ultra` 能力。
- [x] 2026-08-30 删除两套仓库外 Ultra 隔离验证状态，并以 `MARKETING_VERSION=0.9.0 BUILD_VERSION=9 ./build-macos.sh` 重建 macOS arm64 App 与 DMG。脚本内全量 Go 测试、macOS 15.0 最低版本检查、ad-hoc 签名校验、DMG staging 签名校验和 SHA-256 生成通过；DMG 校验和复核通过。
- [x] 2026-08-30 为 Codex `gpt-5.6-sol` Standard/Fast 增加 `ultra`：订阅目录只在 Sol 两条逻辑路由声明该档位；供应商编辑页可选择并保存已声明的默认推理强度，其他托管字段继续受保护。全量 Go、vet、受影响包 race、前端 Node、Wails-tag 与隔离管理 Profile 的桌面/移动浏览器验证通过。
- [x] 2026-08-28 构建并正式启用本机 `0.9.0 (build 9)` ad-hoc 候选；旧 0.8.0 已可逆备份。真实 CodeBuddy Profile 同步 16 个 WorkBuddy CLI 模型但保持未激活，默认仍为 Codex Sol；Kimi-K3、Hy4、GLM-5.3、GLM-5.3-Flash 真实页面连通、订阅代理回归、路由一致性和桌面/移动布局验证通过。
- [x] 2026-08-28 将 WorkBuddy 最新模型目录接入 Grok Build Switch：新增 `GET /api/codebuddy/models`、目录来源/更新时间展示和显式“刷新模型目录”，只读采用 WorkBuddy 本机 `cli` 可用且支持 tool call 的模型；推理继续由 Switch 直连 CodeBuddy，不经过 WorkBuddy harness。已验证 `kimi-k3-2`、`hy4-preview`、`glm-5.3`、`glm-5.3-flash` 直连 HTTP 200；前端、全量 Go、vet、受影响包 race、Wails-tag 与隔离实例桌面/移动浏览器验证通过。
- [x] 2026-08-19 构建并安装本机新版 `0.8.0 (build 8)` ad-hoc App；旧 `/Applications/Grok Build Switch.app` 已删除并替换。新版运行后通过管理页面事务性保存 Codex GPT `context_window=320000`，保留 `low` 等已声明推理档位与 Standard/Fast 元数据，默认供应商恢复为 Kimi；浏览器、全量 Go、前端 Node、vet 与 race 验证通过。
- [x] 2026-08-19 将 Codex/GPT-5.6 订阅代理默认 `context_window` 从 272000 调整为 320000；修复订阅代理 Profile 可在「编辑供应商」页仅调整上下文窗口，同时保留托管元数据并继续拒绝其他字段绕过订阅代理流程。前端 Node、全量 Go、vet、受影响包 race 与隔离实例浏览器验证通过。
- [x] 2026-08-19 审查、验证并发布当前 `main` 到 GitHub；待提交文件的密钥/私有路径扫描无发现，前端 Node、全量 Go、vet、受影响包 race 与 Wails-tag 测试通过，构建产物和本机运行数据未进入 Git。
- [x] 2026-08-19 修复 CodeBuddy 托管 Profile 的供应商后缀别名污染与默认模型子集缺失；本机 Profile 经管理 API 修复，Flash/Pro 代理调用恢复。实测两者网关上限为 1,048,576 tokens，配置窗口按用户决定设为 1,000,000；约 1,039,948 prompt tokens 的开头与中部标记均能准确召回。
- [x] 2026-08-18 建立根目录 AGENTS.md / Status.md / Plan.md，并与 docs/ 现有文档建立引用关系。

## 使用约定

- 开始新任务时在「当前任务」写下目标、步骤清单与验收标准。
- 每完成一步勾选并记录阻塞；遇到与 Status.md 边界冲突的事项先停下来报告。
- 任务结束：勾选全部步骤 → 移入「已完成」→ 更新 Status.md。
