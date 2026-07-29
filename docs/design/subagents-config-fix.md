# Subagents 配置纠错与改造说明

> 状态：设计文档（**已实现** explore/plan → `[subagents.models]`）  
> 依据：Grok Build 用户指南 `~/.grok/docs/user-guide/05-configuration.md`、`16-subagents.md`  
> 对照代码：`internal/config/tomlio.go`、`internal/profiles/model.go`、`ui/index.html`、`ui/app.js`  
> 相关：`problems.md` 问题 3  

---

## 1. 结论（先看这个）

| 问题 | 说明 |
|:---|:---|
| **哪里错了？** | grok_switch 写入了 Grok **不识别**的键：`[subagents] default_model = "..."` |
| **正确写法？** | 按子代理**类型**分别写在 **`[subagents.models]`** 下 |
| **UI 应怎样？** | 分别显示 **explore**、**plan** 两个名字，各自选模型，再写入对应键 |
| **你设想的写入是否正确？** | **是的**，例如： |

```toml
[subagents.models]
explore = "grok-4.5"
plan = "grok-4.5"
```

说明：

- 这是 Grok 官方支持的「按类型指定模型」方式。
- 不写某类型时，该类型 **继承父会话模型**（通常即 `[models].default`）。
- 所选模型 ID 必须在已启用的 `[model."..."]` 段中可解析（有 api_key / base_url 等）。

---

## 2. 官方 Subagents 是什么

Grok Build **支持** Subagents（默认开启）。主 Agent 通过 `spawn_subagent` 启动子会话。

内置类型（文档）：

| 类型名 | 作用 |
|:---|:---|
| `general-purpose` | 默认全能子代理 |
| `explore` | 只读调研：搜索/读文件/grep/shell，**不改文件** |
| `plan` | 规划：探代码库、出实现计划，**不改文件** |

相关（可选，当前 switch **不必**全部实现）：

| 配置 | 作用 |
|:---|:---|
| `[subagents] enabled` | 总开关 |
| `[subagents.toggle].explore` 等 | 按类型启用/禁用 |
| `[subagents.models].explore` 等 | **按类型指定模型** ← 与 switch 最相关 |
| `[subagents.personas.*]` | 行为 persona |
| `[subagents.roles.*]` | 自定义 role |

**没有**官方文档中的全局键：`subagents.default_model`。

---

## 3. 当前 grok_switch 错在哪里

### 3.1 错误写入（实际生效到 config.toml 的形态）

启用供应商后，当前逻辑会写成类似：

```toml
[subagents]
default_model = "grok-4.5"
```

Grok CLI 日志：

```text
WARN config has unrecognized key(s): subagents.default_model. Run /help for config reference.
```

后果：

1. 用户以为「Subagents 模型」已切换 → **实际未生效**。
2. 子代理仍走默认行为：多数情况下 **继承父会话主模型**，与 UI 选择可能不一致。
3. 配置里多一条无用键，产生噪音 WARN。

### 3.2 错误点分布（代码 / UI / 文档）

| 位置 | 当前行为 | 问题 |
|:---|:---|:---|
| `tomlio.go` → `rewriteSection("subagents")` | 只写 `default_model` | 键名错误 |
| `tomlio.go` → `ApplyProfile` | `subagents["default_model"] = ...` | 同上 |
| `tomlio.go` → `SnippetForProfile` | 预览片段含 `default_model` | 预览也错误 |
| `tomlio.go` → `ImportProfile` | 从 `subagents.default_model` 读 | 读错键；读不到 `models.explore/plan` |
| `tomlio.go` → `UseOfficialAuthText` | 仅删除 `default_model` | 将来若写了 `models`，清理策略需同步 |
| `profiles.Profile` | 字段 `SubagentsDefaultModel` / `subagents_default_model` | 单一全局值，无法区分 explore/plan |
| `ui/index.html` | 一个下拉「Subagents 模型」 | 无法分别配置 |
| `ui/app.js` | 读写 `subagents_default_model` | 同上 |
| Grok Auth 默认 profile | 默认填一个 `SubagentsDefaultModel` | 会继续写错误键 |
| README / docs | 写「写入 `[subagents].default_model`」 | 文档与官方不一致 |
| 测试 `tomlio_test.go` | 断言 `default_model` 被写入/替换 | 把错误行为测成了「正确」 |

### 3.3 错误 vs 正确（并排）

**错误（当前）：**

```toml
[subagents]
default_model = "grok-4.5"
```

**正确（目标）：**

```toml
[subagents.models]
explore = "grok-4.5"
plan = "grok-4.5"
```

两个模型可以相同，也可以不同，例如：

```toml
[subagents.models]
explore = "grok-4.5"              # 调研用主模型
plan = "grok-composer-2.5-fast"   # 规划用更快/更便宜模型
```

---

## 4. 正确配置应该是什么样子

### 4.1 最小正确片段（推荐 switch 只写这些）

当用户为 explore / plan 都选了 `grok-4.5` 时：

```toml
[subagents.models]
explore = "grok-4.5"
plan = "grok-4.5"
```

当用户只配了 explore、plan 留空时：

```toml
[subagents.models]
explore = "grok-4.5"
# 不写 plan → plan 继承父会话模型
```

当两个都留空时：

- **不要写** `[subagents.models]` 段（或不要写 switch 管理的 explore/plan 键）  
- 子代理全部继承 `[models].default` 对应模型  

### 4.2 与主模型、联网模型一起的完整示例

```toml
[endpoints]
models_base_url = "https://api.example.com/v1"

[models]
default = "grok-4.5"
web_search = "grok-4.5"
default_reasoning_effort = "high"

[model."grok-4.5"]
model = "grok-4.5"
base_url = "https://api.example.com/v1"
api_key = "sk-..."
api_backend = "responses"
supports_backend_search = true

# ✅ 正确：按类型指定子代理模型
[subagents.models]
explore = "grok-4.5"
plan = "grok-4.5"
```

### 4.3 官方还可选（switch 本轮可不写）

```toml
[subagents]
enabled = true

[subagents.toggle]
explore = true
plan = true
```

### 4.4 不要再写

```toml
# ❌ 错误：Grok 不识别
[subagents]
default_model = "grok-4.5"
```

---

## 5. UI 应该怎么改（产品约定）

### 5.1 原则

- UI 上应 **分别展示** 两个内置子代理类型的名字：**explore**、**plan**。
- 各自有一个模型下拉（选项与「已启用模型」列表一致，可含「（空 = 继承主模型）」）。
- 启用供应商时，写入：

```toml
[subagents.models]
explore = "<explore 所选>"
plan = "<plan 所选>"
```

- 仅写入非空项；空项不写对应键。

### 5.2 UI 示意

| 标签（显示名） | 类型键 | 控件 | 写入 TOML |
|:---|:---|:---|:---|
| explore 子代理模型 | `explore` | 下拉 | `[subagents.models] explore = "..."` |
| plan 子代理模型 | `plan` | 下拉 | `[subagents.models] plan = "..."` |

文案建议：

- 标题：`Subagents 模型`（区块标题）
- 子项：`explore`、`plan`（可用副标题说明：调研只读 / 规划只读）
- 空选项：`（继承主模型）`

### 5.3 你确认的写入形态（正确）

```toml
[subagents.models]
explore = "grok-4.5"
plan = "grok-4.5"
```

**是的，这样是对的。**

补充：

| 情况 | 写入 |
|:---|:---|
| 两个都选 `grok-4.5` | 如上两行 |
| explore=`A`，plan=`B` | 两行不同模型 ID |
| 只选 explore | 只写 `explore = "..."` |
| 都空 | 不写 `[subagents.models]` 中由 switch 管理的键 |

### 5.4 关于 `general-purpose`

- 官方还有 `general-purpose` 类型。
- 本轮 UI **至少**做 explore + plan（文档与使用频率最高）。
- 若后续扩展，可同样增加一行：`general-purpose = "..."`。

---

## 6. 数据模型建议（改代码时用，本文不实现）

### 6.1 Profile 字段（建议）

当前（错误语义）：

```text
subagents_default_model: string   // 单一全局
```

建议（正确语义）：

```text
subagents_models: {
  explore?: string
  plan?: string
  // 可选: "general-purpose"?: string
}
```

兼容迁移：

- 若旧 profile 只有 `subagents_default_model = "X"`：  
  导入时展开为 `explore = X`、`plan = X`（或仅提示用户重新选择）。
- 写盘时 **禁止** 再输出 `default_model`。

### 6.2 读写 config 规则

| 操作 | 应做 |
|:---|:---|
| 启用 / ApplyProfile | 写入/更新 `[subagents.models]` 的 explore、plan；**删除**残留的 `default_model` |
| 预览 Snippet | 展示正确的 `[subagents.models]` 片段 |
| 导入 Import | 读 `[subagents.models]`；兼容读旧 `default_model` 仅作迁移 |
| 切官方 | 清理 switch 写入的 explore/plan 模型键（及旧 `default_model`） |
| Matches 漂移检测 | 比较 explore/plan 模型字符串，而非单一 default |

### 6.3 测试应改的方向

- 断言 **不再** 出现 `default_model`。
- 断言启用后存在：

```toml
[subagents.models]
explore = "..."
plan = "..."
```

- 覆盖：只配一个类型、两个都空、官方切换清理。

---

## 7. 实现时涉及文件清单（备忘，未改）

| 文件 | 改动要点 |
|:---|:---|
| `internal/profiles/model.go` | 字段改为 map / 双字段；Normalize / Matches |
| `internal/config/tomlio.go` | 写/读/预览/官方清理 |
| `internal/config/tomlio_test.go` | 断言新 TOML 形态 |
| `internal/server/grokauth.go` | Grok Auth 默认 profile 的 subagent 默认值 |
| `ui/index.html` | 两个下拉：explore / plan |
| `ui/app.js` | 表单读写、预览、模板 |
| `README.md`、`docs/*`、`problems.md` | 文档与已知问题更新 |

---

## 8. 总表：错误 vs 正确

| 项 | 当前（错） | 目标（对） |
|:---|:---|:---|
| TOML 段 | `[subagents]` | `[subagents.models]` |
| 键 | `default_model` | `explore`、`plan`（按类型） |
| 示例 | `default_model = "grok-4.5"` | `explore = "grok-4.5"` / `plan = "grok-4.5"` |
| UI | 一个「Subagents 模型」 | 两个：**explore**、**plan** |
| Profile JSON | `subagents_default_model` | `subagents_models.explore` / `.plan` |
| 空值含义 | 仍可能写入空/旧键 | 不写该类型键 → 继承主模型 |
| Grok 行为 | WARN，配置无效 | 按类型路由模型 |

---

## 9. 一句话总结

- **错：** 全局假键 `subagents.default_model` + UI 单一「Subagents 模型」。  
- **对：** UI 分别配置 **explore** / **plan**，启用时写入：

```toml
[subagents.models]
explore = "..."
plan = "..."
```

该形态与 Grok 官方文档一致；你提出的 UI 与写入方式是正确目标。

---

## 10. 参考

- 本机：`~/.grok/docs/user-guide/05-configuration.md`（Subagents 小节）  
- 本机：`~/.grok/docs/user-guide/16-subagents.md`  
- 仓库：`problems.md`（问题 3）  
- 仓库：`internal/config/tomlio.go`（当前错误写入入口）
