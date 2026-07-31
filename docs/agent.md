# Grok Build Switch — 维护者文档

> 面向维护者的当前架构、产品边界与数据安全约定。最后更新：2026-07-30。

---

## 1. 项目定位

Grok Build Switch 是一个 macOS 桌面应用，用于管理 Grok CLI 官方登录路由、普通 Profile、统一模型路由、订阅代理和 `~/.grok/config.toml`，并提供 LAN、SSH、菜单栏与 Wails 入口。

### 1.1 真相源与工作区边界

- GitHub `CyberStaZJU/grok-build-switch` 是代码、测试、轻量文档和发布记录的权威源。
- 本地未提交修改在 commit/push 前是不可替代的工作，不得重置或覆盖。
- 构建产物、日志、凭据和运行配置不进入 Git。
- 修改当前产品文档时，不回写历史博客或历史设计文档。

### 1.2 当前产品边界

保留能力：

- Grok CLI 官方登录与官方路由；
- 普通 Profile；
- default、web_search、explore、plan 统一模型路由；
- CLIProxyAPI 订阅代理；
- `config.toml` 查看、校验与编辑；
- LAN、SSH、macOS 菜单栏和 Wails。

以上清单是完整产品范围。清单之外的旧扩展已移除；维护者不得在无新产品决策时重新接线，也不得在当前说明中保留其功能、接口或数据目录清单。

---

## 2. 入口与核心职责

| 入口 | 作用 |
|:---|:---|
| 默认应用入口 | 启动本地 HTTP 服务、菜单栏和浏览器管理页 |
| Wails 入口 | 启动桌面窗口并复用同一服务能力 |
| 菜单栏 | 打开窗口、查看状态和执行常用路由操作 |

两个桌面入口都应只初始化当前保留能力。入口行为必须保持一致，不得因构建标签重新暴露已移除范围。

核心职责可以概括为：

```text
Profiles ─┐
          ├─> unified routing ─> ~/.grok/config.toml
Official ─┘

Subscription proxy ─> supported proxy routes
LAN / SSH          ─> protected remote management
Menu bar / Wails   ─> local desktop access
```

---

## 3. 路由事务

路由更新应遵守：

```text
HTTP request
  → strict decode
  → merge current policy
  → validate profile/model selection
  → preview target config
  → atomically write config.toml
  → persist routing policy
  → roll back this write on persistence failure
```

重要约束：

- `routing.json` 表示统一路由策略，`config.toml` 表示 Grok CLI 执行配置；
- 官方认证由 Grok CLI 官方登录流程管理；
- 路由更新只处理配置与模型选择；
- 普通 Profile UI 只管理基础连接信息和常用模型；
- UI 不超出当前产品范围。

---

## 4. 数据边界与清理授权

### 4.1 保留数据

以下数据属于当前产品或 Grok CLI，必须保留：

- `~/.grok/config.toml`；
- Grok CLI 官方认证；
- 普通 Profile；
- 统一路由策略；
- 订阅代理账号、令牌和凭据；
- LAN、SSH 和当前应用设置。

### 4.2 已授权清理

用户已明确授权清理已移除能力遗留在应用 DataDir 中的旧记录。实现或执行清理时必须使用可审计的范围判断：

1. 仅处理能够确认属于已移除能力的应用自有记录；
2. 将 Grok CLI 官方认证列入硬性排除项；
3. 将订阅代理账号、令牌和凭据列入硬性排除项；
4. 不以整个目录为单位扩大删除范围；
5. 对未知文件或混合数据停止自动删除并报告。

不要在当前文档中保留已移除能力的具体旧路径或接口清单；清理实现应以代码中的受控迁移规则和测试为准。

---

## 5. HTTP 与安全边界

当前 HTTP 服务只应暴露保留功能所需的管理和代理能力。新增或修改状态的端点必须遵守：

- 默认本机监听；
- LAN 配对与可信来源检查；
- 修改请求的 CSRF 防护；
- 请求体大小限制和严格解码；
- 响应脱敏，禁止返回 API Key、OAuth token 或私有 header；
- 不新增已移除能力的兼容端点。

---

## 6. 前端约定

- 当前 UI 聚焦官方登录、普通 Profile、统一模型路由、订阅代理、配置编辑、LAN 和 SSH。
- 菜单栏与 Wails 必须提供一致的当前能力。
- UI 入口、状态卡和表单只覆盖当前产品范围。
- 非原生搜索模型不得被描述为自动获得额外搜索工具。
- 历史博客与设计文档不作为当前 UI 需求来源。

---

## 7. 构建与验证

常规变更至少验证：

```bash
node --check ui/app.js
go test ./...
go test -tags wailsgui .
```

产品边界相关变更还应覆盖：

- 官方登录和官方路由；
- 普通 Profile 与统一路由事务；
- 订阅代理及其凭据保留；
- `config.toml` 编辑；
- LAN、SSH、菜单栏和 Wails；
- DataDir 遗留清理不会触及 Grok CLI 官方认证或订阅代理凭据；
- 当前 UI 和当前产品文档不出现已移除能力。

发布前运行构建、签名/公证检查和安装包 smoke test。构建产物必须留在 `dist/` 或仓库外，不提交 Git。
