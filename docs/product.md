# Grok Build Switch 产品文档

> 面向用户与维护者的当前产品边界、数据和使用说明。最后更新：2026-07-30。

---

## 1. 产品定位

Grok Build Switch 是一个本地 macOS 菜单栏/桌面工具，用于管理 Grok CLI 的官方登录路由、`~/.grok/config.toml`、普通供应商 Profile 和统一模型路由。

核心能力：

- 通过 Grok CLI 官方流程登录，并在登录后应用官方模型路由；
- 以普通 Profile 管理供应商、Base URL、API Key、上游格式和常用模型；
- 统一配置 `default`、`web_search`、`subagents.explore`、`subagents.plan`；
- 通过内嵌 CLIProxyAPI 接入受支持的订阅代理；
- 查看、校验和编辑 `~/.grok/config.toml`；
- 提供 LAN 配对访问和 SSH 连接管理；
- 通过 macOS 菜单栏或 Wails 窗口提供管理界面。

### 1.1 当前边界

本节核心能力清单是当前产品的完整范围。清单之外的旧扩展已移除，不再作为当前功能、接口或数据目录列出。

历史博客和历史设计文档可能描述旧版本能力，它们只用于版本追溯，不代表当前产品行为。

---

## 2. 系统架构

```text
Browser management UI / Wails / macOS menu bar
                       │
                       ▼
                  HTTP Server
                       │
       ┌───────────────┼────────────────┐
       ▼               ▼                ▼
    Profiles        Routing          Services
 ordinary data   unified policy   subscription/LAN/SSH
                       │
                       ▼
             ~/.grok/config.toml
```

### 2.1 配置真相

- 普通 Profile 保存可选供应商和模型定义；
- 统一路由策略保存当前模型选择；
- `~/.grok/config.toml` 是 Grok CLI 实际执行配置；
- 官方认证由 Grok CLI 官方登录流程管理；
- 路由更新必须保持统一路由策略与 `config.toml` 一致。

### 2.2 桌面入口

- **macOS 菜单栏**：打开管理界面、查看状态和执行常用切换；
- **Wails 窗口**：提供桌面管理界面；
- **本地浏览器管理页**：与桌面入口共享服务端能力；
- **LAN**：经配对后访问受保护的管理功能；
- **SSH**：管理远程连接和文件操作。

---

## 3. 核心功能

| 功能 | 当前行为 |
|:---|:---|
| 官方 Grok CLI | 使用官方登录流程，验证登录状态并应用官方模型路由 |
| 普通 Profile | 管理供应商、Base URL、API Key、上游格式和常用模型 |
| 统一模型路由 | 管理 default、web_search、explore 和 plan |
| 订阅代理 | 管理内嵌 CLIProxyAPI 的生命周期、登录和代理路由 |
| `config.toml` 编辑 | 查看、校验并编辑 Grok CLI 当前配置 |
| LAN | 提供配对、来源检查和 CSRF 保护 |
| SSH | 管理连接及远程文件操作 |
| 菜单栏 / Wails | 提供本地桌面入口和状态控制 |

### 3.1 普通 Profile

Profile 用于保存一个上游所需的基础信息：

- 显示名称；
- Base URL；
- API Key；
- 上游协议或格式；
- 可用于统一路由的模型列表。

Profile 保持普通、可理解的基础模型选择界面。

### 3.2 统一模型路由

统一路由包含：

```text
official
default
web_search
subagents.explore
subagents.plan
```

路由更新执行严格校验、目标配置预览、原子写入和失败回滚。该流程只处理配置与模型选择。

### 3.3 官方路由

- 未登录时，应用调用 Grok CLI 官方登录流程；
- 登录完成后，用户可再次选择官方路由；
- 应用不得以自身 DataDir 记录替代或托管 Grok CLI 官方认证；
- 应用的旧记录清理不得删除、重置或改写官方认证。

### 3.4 订阅代理

订阅代理由内嵌 CLIProxyAPI 提供。订阅代理账号、令牌和相关凭据属于保留功能的运行数据，不属于本轮遗留清理目标。

### 3.5 `config.toml` 编辑

应用允许用户查看、校验和编辑 `~/.grok/config.toml`。统一路由写入与手动编辑都必须尽量保持配置可解析，并在失败时避免留下半写入状态。

---

## 4. 数据与安全

### 4.1 数据分类

| 数据 | 所属 | 清理边界 |
|:---|:---|:---|
| `~/.grok/config.toml` | Grok CLI 执行配置 | 保留；仅按用户配置操作更新 |
| Grok CLI 官方认证 | Grok CLI 官方登录 | 禁止作为应用旧记录删除或改写 |
| 普通 Profile | 当前产品 | 保留 |
| 统一路由策略 | 当前产品 | 保留 |
| 订阅代理账号与凭据 | 当前产品 | 保留，禁止纳入旧记录清理 |
| LAN 配对和 SSH 配置 | 当前产品 | 保留 |
| 应用设置和日志 | 当前产品运行数据 | 按各自策略处理 |
| 已移除能力的旧 DataDir 记录 | 旧产品能力 | 用户已授权清理，但必须确认归属 |

### 4.2 已授权的遗留清理

用户已授权清理已移除能力遗留在应用 DataDir 中的旧记录。该授权是范围明确的清理授权，不是删除整个 DataDir 的授权。

执行清理时：

1. 只删除能够确认归属于已移除能力的应用自有记录；
2. 明确排除 Grok CLI 官方认证；
3. 明确排除订阅代理账号、令牌和凭据；
4. 明确排除普通 Profile、统一路由、LAN、SSH 和当前应用设置；
5. 无法确认归属时保留并报告。

### 4.3 通用安全要求

- 默认只监听本机地址；
- LAN 请求必须经过配对、来源和 CSRF 检查；
- 状态响应和日志不得暴露 API Key、OAuth token 或私有 header；
- 敏感文件应使用用户私有权限和原子写入；
- 不向 Git 提交本机凭据、运行数据或构建产物。

---

## 5. 使用流程

### 5.1 使用官方 Grok CLI

1. 在应用中选择官方 Grok CLI；
2. 如尚未登录，完成 Grok CLI 官方登录流程；
3. 返回应用并重新应用官方路由；
4. 在终端启动 Grok CLI 验证配置。

### 5.2 使用普通 Profile

1. 新建 Profile；
2. 填写名称、Base URL、API Key 和上游格式；
3. 获取或填写模型列表；
4. 保存 Profile；
5. 在统一模型路由中选择 default、web_search、explore 和 plan；
6. 保存并检查 `~/.grok/config.toml`。

### 5.3 使用订阅代理

1. 打开订阅代理管理；
2. 按受支持流程登录订阅账号；
3. 确认代理状态正常；
4. 将代理提供的模型加入统一路由。

### 5.4 使用 LAN、SSH 和桌面入口

- 通过菜单栏快速打开应用和查看状态；
- 使用 Wails 窗口进行完整管理；
- 开启 LAN 前完成配对并确认网络边界；
- 通过 SSH 配置连接后执行受支持的远程操作。

---

## 6. 构建与验证

系统目标：macOS 15+，Apple Silicon / arm64。

```bash
./build-macos.sh
```

常规验证应覆盖：

- 普通 Profile 与统一路由；
- Grok CLI 官方登录和官方路由；
- 订阅代理；
- `config.toml` 编辑；
- LAN、SSH、菜单栏和 Wails；
- DataDir 清理不触及官方认证或订阅代理凭据。

构建产物位于 `dist/`，不应提交 Git。

---

## 7. 常见问题

### 官方路由无法启用

确认 Grok CLI 已安装，并完成其官方登录流程。应用不会用自身托管的认证记录替代官方登录。

### 路由保存失败

检查 Profile、模型选择、`~/.grok/config.toml` 写权限和统一路由存储状态。失败后确认配置未处于半写入状态。

### web_search 路由不可用

请选择实际支持原生搜索的模型，或在 Grok CLI 自身配置所需 MCP。应用不会把不支持搜索的模型描述为可用。

### 能否清理旧 DataDir 记录

可以。用户已授权清理已移除能力的应用自有旧记录，但必须保留 Grok CLI 官方认证和订阅代理凭据，并保留无法确认归属的数据。
