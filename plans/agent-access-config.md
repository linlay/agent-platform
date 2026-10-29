# Agent 级 accessConfig：默认权限档位

## 背景

现在 Run 的权限档位（`default` / `auto_approve` / `full_access`）只能由每次请求带进来，或者从父 Run 继承。agent.yml 没有地方声明「这个 Agent 默认用什么档位」，全局 `tools.yml` 也只定义档位内容，没有默认值或上限。

Frank 希望在 agent.yml 里给每个 Agent 配一个默认档位，持久化在 yml 里；被调度起来的子 Agent 按自己 yml 的档位运行，而不是沿用父 Run 的档位。

本文档记录现状、已确定的需求、评估中发现的问题和待定事项，以及需要改动的地方。行号以写作时的 main（`6b4dd6ab` 之后）为准。

## 现状

### 三个档位与审批矩阵

- 档位定义与矩阵：`docs/AccessPolicy与HITL改造.md:17`、`:25-41`。
- 常量与归一化：`internal/contracts/interfaces.go:23-25`；`NormalizeAccessLevel`（`:44`）把空值归一成 `default`。
- 档位排序与 run start 解析：`internal/contracts/run_start.go:57`（rank）、`:70`（`ResolveRunStartAccessLevel`）、`:84`（请求摘要使用原始值）、`:93`。
- 默认档位内容：`internal/config/config_defaults.go:239-285`
  - `default`：读 `@workspace,@chat,@agent,@skills,@temp`，写 `@chat,@temp`，`@agent,@skills` 只读，根外写入 HITL。
  - `auto_approve`：在 default 基础上根外读取自动通过；根外写入、破坏性操作、可执行配置、远程变更仍 HITL。
  - `full_access`：`@root` 读写，`ReadonlyRoots` 为空，全部 allow。
- `@agent` 只指向当前 Agent 自己的目录（`internal/accesspolicy/accesspolicy.go:659`）。

### 没有全局默认，也没有上限

`config.AccessPolicyConfig` 只有 `Levels`（`internal/config/config.go:580-605`），没有 default 字段，也没有 cap 字段。最终的默认值就是 `NormalizeAccessLevel` 的空值兜底 `default`。

### 各入口如何决定档位

| 入口 | 未显式传档位时 | 显式传档位时 | 位置 |
|---|---|---|---|
| HTTP query（WebClient / Desktop） | `default` | 用请求值 | `internal/runtime/query/admission.go:360-364`；`session/session_builder.go:267,486` |
| 运行中切换档位 | — | 改当前 Run 的 RunControl 档位，不写回 yml | `server/handler_execute.go:76`；`runstate/manager.go:166,341,355` |
| side query | 继承所在 Run | — | `runtime/query/side_query.go:39`；`runtime/query/access_level.go` |
| `chat_start` / `chat_send` | 继承父 Run 当前档位 | 高于父 Run 需人工审批 | `runtime/query/chat_tools_facade.go:59-127,135,154-161`；`runtime/query/mailbox.go:228-260` |
| `agent_invoke` / Team 子任务 | 使用父会话档位 | 不支持单独指定 | `internal/orchestration/coordinator.go:548,758` |
| Automation | 读 `query.accessLevel`；YAML 省略时为 `default`；工具创建省略时显式写入 `default` | 用保存值，不经过 chat_start 的提升审批 | `automation/registry.go:380,763`；`automation/service.go:364`；`automation/control.go:152-155`；`docs/自动化.md:9,52,56,97` |
| PROXY / ACP | 透传 Run 档位 | — | `runtime/proxy/protocol.go:72`；`runtime/proxy/driver.go:331` |

### interactionConfig 里已有一个 accessLevel 开关

`interactionConfig.accessLevel` 是布尔开关，控制前端是否允许用户选档位（`internal/interaction/config.go:16-27,76`）。它会拒绝未知字段（`:116`），校验在 `:131`。因此新的默认档位不能塞进 `interactionConfig`，否则既和布尔语义冲突，也过不了严格校验。

### 前端行为

- WebClient 总是发送 `accessLevel`：`src/features/composer/lib/composerAccessLevel.ts:41-46` 从 sessionStorage 读，取不到就兜底 `"default"`；`ComposerArea.tsx:787,900` 无条件带上。
- Desktop 只在用户设置了档位时才发送：`zenmind-desktop/src/main/modules/agent-platform/run-controller.ts:184,199,453,478`；看板 `kanban/run-policy.ts:59`。
- WebClient Agent 编辑器保存时展开原 definition（`agentDefinition.ts:754,772,787`），未知顶层键能保留；但「重置」会清空 `interactionConfig`（`AgentEditor.tsx:589`）。

### 可参照的先例：modelConfig.maxInputTokens

一个 Agent 级字段从 yml 到运行时的完整链路：`catalog/agent_type.go:342,353-375` → `catalog/agent_loader.go:631` → `catalog/registry.go:56` → `session/session_builder.go:212`；编辑器 `catalog/agent_editor.go:572`；管理端 `adminsource/model_input_limit.go`；WebClient `agentDefinition.ts:795-801`。

### yml 的读写

- 顶层未知键被 `parseAgentTree` 静默忽略（`catalog/agent_loader.go:586`）。所以今天就算有人写了 `accessConfig`，也不会生效，也不会报错。
- 结构化保存会整体重写 YAML：`catalog/agent_editor.go:211`（`UpdateEditableAgent`）、`:440`（`normalizeEditableDefinition`）、`:470`（`persistEditableAgent`）、`:563-575`（`agentYAMLKeyOrder`，根顺序为 `modelConfig`、`budget`、`interactionConfig`…）、`:577`（`renderYAMLMap`，注释丢失）。
- 原文写入走 `WriteEditableAgentSource`（`agent_editor.go:80`），保留原文。
- 管理端入口：`server/handler_catalog.go:286`（创建校验）、`:442`（`updateAgent`，整份 definition）、`:491`（`updateAgentModelConfig`，只改 modelConfig，其余字段沿用原 definition）；`server/handler_admin_source.go:151,191`（原文写入）；`server/handler_admin_agent_import.go:25,63`（归档导入）。

## 目标功能（已确定）

### 1. agent.yml 新增 accessConfig 块

```yaml
modelConfig:
  ...
accessConfig:
  default: auto_approve   # 字段名 default / defaultLevel 待定
```

- 放在 `modelConfig` 后面，`agentYAMLKeyOrder` 也要把 `accessConfig` 排在 `modelConfig` 之后。
- `accessConfig.default` 设定该 Agent 的默认档位，持久化在 yml。
- 这个块以后还可能放上限（cap）之类的配置，所以单独成块，不放进 `interactionConfig`。
- 字段名用 `default` 还是 `defaultLevel`：**待定**。

### 2. 子 Agent 不传档位时用自己 yml 的档位（行为变更）

`chat_start`、`chat_send`、`agent_invoke`、Team 子任务没有显式指定档位时，子 Run 使用**子 Agent 自己 yml** 里的 `accessConfig.default`，父 Run 的档位不参与。

**这是行为变更**：现在这几个入口是「继承父 Run 当前档位」（`chat_tools_facade.go`、`coordinator.go:548,758`、`docs/子智能体调度.md:79,82,84,89,91`），改完后变成「看子 Agent 自己的配置」。子 Agent 没配 `accessConfig` 时回落到 `default`。

### 3. 父 Run 显式指定的档位不能超过父 Run 自己的当前档位

父 Run 显式给子 Run 指定档位时，不得高于父 Run 当前档位。

- 超过时是直接拒绝，还是保留现在的「人工审批后放行」（`chat_tools_facade.go:154-161` 的 `run_start_approval_required`）：**待定**。

### 4. Agent 通过 catalog_manage.apply 修改 accessConfig 一律人工审批

任何 Agent 用 `catalog_manage.apply` 修改**任何** Agent 的 `accessConfig`（新增、修改、删除该块），无论调用方处于哪个档位，都必须人工审批，和 delete 一样不能自动批准。

管理员在管理端界面里改、或者直接改 yml 文件，不受这条限制。

## 发现的问题

### P1. admission 先归一化、后解析 Agent，顺序要调换

`admission.go:360-364` 先把请求档位归一化（空值已经变成 `default`），`:440-454` 才解析出 `agentDef`，`:487` 再做 interaction 校验。到能读 `accessConfig` 时，「请求没传」这个信息已经丢了。需要保留原始值，在拿到 `agentDef` 之后再决定最终档位。

### P2. WebClient 总是发 accessLevel，只改服务端不生效

WebClient 每次都带 `accessLevel`（兜底 `"default"`），服务端无法区分「用户选了 default」和「用户没选」。只改服务端，WebClient 发起的聊天永远拿不到 yml 默认值。WebClient 需要在用户没手动选过档位时不发，或者初始值取 Agent 的默认档位。Desktop 本来就在未设置时不发，问题较小。

### P3. apply 自动批准 + 需求 2 = 自我提权漏洞

现状：`catalog_manage.apply` 在 `auto_approve` / `full_access` 下自动批准（`docs/Platform控制工具设计.md:93`；代码 `internal/platformcontrol/control_handler.go:162`，`allowAutoApprove := tool == "catalog_manage" && action == "apply"`；统一判断在 `internal/llm/run_stream_tool_approval.go:74-79`）。

叠加需求 2 后：一个 `auto_approve` 的 Agent 可以自动批准地 apply 一个新 Agent（或修改另一个 Agent），把它的 `accessConfig.default` 设成 `full_access`，然后 `chat_start` 它且不传档位，子 Run 就以 `full_access` 运行，绕过了现在「高于父 Run 需人工审批」的保护。

`adminsource` 已经禁止调用方 apply 自己（`internal/adminsource/control.go:438-447`，含 SameFile 检查），但它挡不住「改别人再调别人」。

需求 4 正是为堵这个口子。需求 4 落地前，需求 2 不能单独上线。

### P4. confirmationRules 不能直接匹配「apply 改了 accessConfig」

结论：**不能**，必须在 Go 里判断。

- `catalog_manage.yml:15-19` 的规则只有 `when: /action: delete` → `resource_delete_review`，其余 → `platform_control_review`。
- `internal/tools/confirmation_rules.go:17-18` 注释写明 confirmationRules 只选择审批视图，是否需要审批由业务 planner 决定（同 `docs/Platform控制工具设计.md:95`）。
- 匹配方式是参数上某个 JSON Pointer 与标量做相等比较（`confirmation_rules.go:53-62,91-98`）。apply 的候选内容是一整段文本，规则看不到「改动前后 accessConfig 是否变化」。

落地方式：在 `PrepareToolApproval`（`control_handler.go:143`）里，对 Agent 主定义的 apply 解析 `plan.Before` / `plan.After`（`adminsource/control.go:431` Prepare，约 `:480-512` 可取前后内容），比较 `accessConfig`，有变化就把 `AllowAutoApprove` 设为 `false`。先例：`library_handler.go:120`（`SourceGrantChanged` 强制人工）、`automation_handler.go:73`。可以顺带在审批表单里加一个「权限配置变更」标记或专用视图，方便用户看清楚。现有的 `permissionFields` 提示（`control_handler.go:182-183`，已包含 `accessLevel`）只是展示，不影响是否自动批准，要把 `accessConfig` 加进去。

### P5. 绕过需求 4 的其他路径

需求 4 只管 `catalog_manage.apply`。Agent 还有别的方式改到 agent.yml，或拿到更高档位：

1. **文件工具 / bash 直接写 yml，watcher 热重载。** `PlatformProtectedPaths`（`internal/accesspolicy/protected.go:19-35`）只包含 StateDir、身份文件、RUPreviewsDir、Desktop `.desktop/secrets`，**AgentsDir 不受保护**（默认 `runtimeRoot/agents`，`config_defaults.go:25`）。改动落盘后目录 watcher 会热重载（`docs/智能体配置说明.md:329`）。
   - `full_access`：`@root` 可写且无只读根（`config_defaults.go:270-284`），可以免审批改任何 `agents/<key>/agent.yml`，包括自己的。
   - `default` / `auto_approve`：其他 Agent 的目录在写根之外，写入走 HITL；自己的目录是只读的 `@agent`。
   - **但是**：本 Run 写到 Chat 目录、再由系统解释器执行的自写脚本在所有档位都免审（`internal/accesspolicy/bash_review.go:165`，`bash-access:authored-script`；`docs/AccessPolicy与HITL改造.md:37`）。宿主机模式下没有操作系统级隔离（同文档 `:5`：获准执行的代码内部行为不受静态分析约束）。所以一个 `default` 档位的 Agent 也可以写一个脚本去改任意 agent.yml，再起子 Agent。这条路径今天就存在，不是 accessConfig 引入的，但需求 2 会让它的收益变大。是否要把 AgentsDir（以及 RU Agent 目录）加进受保护路径：**待定**。
2. **automation_manage 已经是一条提权路径。** `automation_manage` 的 create / update / trigger 在 `auto_approve` / `full_access` 下自动批准（`internal/platformcontrol/automation_handler.go:73`，`AllowAutoApprove: action != "delete"`），`query.accessLevel` 可以显式写 `full_access`，且「不经过 chat_start 的权限提升审批」（`docs/自动化.md:97`）。也就是说，`auto_approve` 的 Agent 今天就能建一个 `full_access` 的定时任务并立即 trigger。（按代码与文档推断，**待实测确认**。）建议和需求 3 一起处理：工具创建的 Automation 档位不得超过调用方当前档位，或改为人工审批。
3. **catalog_manage.import**：自动批准（`platformcontrol/archive_import.go:139`），但资源类型只允许 `skill`、`connector`（`platformcontrol/control_validation.go:47-49`），不能导入 Agent。目前不构成绕过，以后放开 Agent 导入时要一并检查 accessConfig。
4. **管理端 HTTP 接口**（`updateAgent`、原文写入、归档导入）：属于管理员路径，按需求 4 不受限制。默认开启鉴权（`config_defaults.go:122-123`；`server/server_auth.go:49`），Agent 用 bash/curl 调用需要有效 token。Agent 运行环境里能不能拿到可用于管理接口的 token（例如注入给 connector 进程的访问令牌）：**待确认**。关闭鉴权的部署里这条路径是敞开的。
5. **项目（isProject）apply 路径、RU Agent 目录**：是否同样经过 `adminsource` Prepare、能否落到 Agent 主定义：**待确认**。
6. **审批没有绑定 accessConfig 的语义**：现在 apply 审批绑定的是内容摘要（`docs/Platform控制工具设计.md:93`），内容变了审批失效，这一点没问题；问题只在 P3/P4 所说的「auto_approve 下根本不出审批」。

### P6. 需求 2 让「用户手动降档」失效

用户在前端把本次 Run 切到 `default`，Agent 却可以 `chat_start` 自己或别的 Agent 且不传档位，子 Run 按 yml 默认值（可能是 `full_access`）运行。用户的降档只对当前 Run 有效，对它派出的子 Run 无效。这是需求 2 的直接后果，需要在文档和 UI 上说清楚；是否额外约束（例如子 Run 不得高于父 Run，除非是 yml 默认值）见待定问题。

### P7. 结构化保存丢注释

`renderYAMLMap`（`agent_editor.go:577`）整体重写 YAML，注释会丢。用户在 yml 里给 `accessConfig` 写的说明注释，经过管理端结构化保存或 `updateAgentModelConfig` 后会消失。原文编辑（`WriteEditableAgentSource`）不受影响。

### P8. 前端切档不写回，和「默认值持久化」的关系

`docs/工具目录权限.md:145-146` 明确运行中切换档位「不写回」。有了 `accessConfig.default` 之后，用户可能期待切档就是改默认值。建议保持切档不写回，另外提供一个显式的「设为默认」操作。

### P9. interactionConfig.accessLevel=false 时的含义

`interactionConfig.accessLevel=false` 表示不允许用户选档位。现在这种情况下请求一律是 `default`。有了 yml 默认值后，应该是「固定使用 yml 默认值」，还是继续固定 `default`：**待定**。

## 待定问题

1. 字段名：`accessConfig.default` 还是 `accessConfig.defaultLevel`。
2. 显式请求（用户在前端选的、父 Run 显式指定的）是否覆盖 yml 默认值。倾向覆盖，但覆盖时是否也受某个上限约束，待定。
3. 需求 3 超限时：直接拒绝，还是保留人工审批放行。
4. `interactionConfig.accessLevel=false` 时用 yml 默认值还是固定 `default`（P9）。
5. Automation 是否也用目标 Agent 的默认值：逻辑上应该用（`query.accessLevel` 省略时取目标 Agent 默认），但工具创建现在会显式写 `default`（`automation/control.go:152-155`），需要一起改，未确认。
6. 只对 Native Agent 生效，还是 PROXY / ACP 也生效（它们只是透传 Run 档位）。
7. 是否需要全局上限（例如部署级最高只允许 `auto_approve`），以及 `accessConfig` 里以后的 cap 和全局上限怎么叠加。
8. 前端切档是否写回 yml：建议不写回，另做「设为默认」（P8）。
9. 子 Run 档位取父子二者较低值：Frank 已表示**不采用**，此处仅作记录。
10. 审计：query 行的档位来源是否增加 `agent_default` 之类的标记，以区分「请求指定」「继承」「Agent 默认」「审批提升」。
11. 结构化保存丢注释是否要处理（P7）。
12. 是否把 AgentsDir / RU Agent 目录加入受保护路径（P5-1），是否同时收紧 automation_manage（P5-2）。

## 改动面

### 后端（agent-platform）

- `catalog/agent_type.go`、`catalog/agent_loader.go`：解析 `accessConfig`，校验档位必须是 `tools.yml` 中存在的档位。
- `catalog/registry.go`：`AgentDefinition` 增加默认档位字段。
- `catalog/agent_editor.go`：`agentYAMLKeyOrder` 在 `modelConfig` 后加入 `accessConfig`；`normalizeEditableDefinition` 规范化该块。
- `server/handler_catalog.go`：创建 / 更新校验 `accessConfig`；确认 `updateAgentModelConfig` 保留原 `accessConfig`。
- `runtime/query/admission.go`：保留请求原始档位，在解析 `agentDef` 之后决定最终档位（P1）。
- `session/session_builder.go`：使用新的最终档位。
- `runtime/query/chat_tools_facade.go`、`runtime/query/mailbox.go`：未显式传档位时用目标 Agent 默认值；显式值按需求 3 校验。
- `orchestration/coordinator.go`：`agent_invoke` / Team 子任务用子 Agent 默认值。
- `automation/*`：按待定问题 5 的结论调整。
- `platformcontrol/control_handler.go`：apply 时比较前后 `accessConfig`，变化则强制人工审批；`permissionFields` 加入 `accessConfig`（需求 4、P4）。
- 视结论：`accesspolicy/protected.go`（P5-1）、`platformcontrol/automation_handler.go`（P5-2）。
- `contracts/run_start.go`：如增加档位来源标记，同步审计字段。

### WebClient（agent-webclient）

- `composerAccessLevel.ts`、`ComposerArea.tsx`：用户没手动选过档位时不发 `accessLevel`，或初始值取 Agent 默认档位（P2）。
- Agent 详情 / 列表接口返回默认档位，档位选择器显示「默认：xxx」。
- `agentDefinition.ts`、`AgentEditor.tsx`：编辑器支持 `accessConfig`，确认「重置」不误清 `accessConfig`。
- `shared/contracts/interaction.ts` 等类型同步。
- 如采用「设为默认」，增加对应操作。

### Desktop（zenmind-desktop）

- `run-controller.ts`、`kanban/run-policy.ts`：未设置时继续不发；UI 展示 Agent 默认档位。

## 测试

- 加载：`accessConfig` 合法 / 非法档位 / 缺省；未知字段行为。
- 编辑器：保存后 `accessConfig` 位于 `modelConfig` 之后；`updateAgentModelConfig` 不丢 `accessConfig`。
- admission：请求不传 / 传 `default` / 传更高档位，与 yml 默认值的组合；`interactionConfig.accessLevel=false` 的组合。
- `chat_start` / `chat_send`：不传时取目标 Agent 默认值（行为变更的回归用例）；显式超过父 Run 档位时按需求 3 的结论处理。
- `agent_invoke` / Team 子任务：子 Agent 默认值生效。
- 需求 4：`auto_approve` / `full_access` 下 apply 新增、修改、删除 `accessConfig` 都出人工审批；不改 `accessConfig` 的 apply 仍自动批准；调用方不能 apply 自己。
- P3 场景端到端：`auto_approve` Agent 写子 Agent 默认 `full_access` 再调用，必须被审批拦住。
- Automation：按待定问题 5 的结论。
- WebClient：未手动选档位时请求不含 `accessLevel`。

## 文档

- `docs/智能体配置说明.md`（`:237`、`:660-690` 附近）：新增 `accessConfig` 说明。
- `docs/AccessPolicy与HITL改造.md`：档位来源顺序。
- `docs/子智能体调度.md:79,82,84,89,91`：继承规则改为「子 Agent 默认值」，标明行为变更。
- `docs/API与协议.md:376,391,405,685,696`：`accessLevel` 省略的语义。
- `docs/Platform控制工具设计.md:93,97`：apply 修改 `accessConfig` 必须人工审批。
- `docs/自动化.md:97`：按待定问题 5 的结论更新。
- `docs/工具目录权限.md:145-146`：切档与默认值的关系。
- `docs/手工测试用例.md:809` 附近：补充手工用例。
