# Agent 级 accessConfig：分析评估与实施方案

> 状态：Platform 与 WebClient 实施完成，后端全量与相关回归通过；前端全量存在下文记录的既有/并行改动断言失败。未部署，外部 ACP/Proxy 目标环境联调尚未执行。
> 评估日期：2026-10-29。Platform 源码基线：`8a514534`；相邻 WebClient / Desktop 以本次本地源码读取为准。
> 本文取代原调研稿。保留已明确的四项需求，对原来的待定项给出具体建议，并区分当前事实与目标行为。

## 1. 结论与范围

建议实施 `accessConfig.default`，但必须把它作为一次权限来源调整，而非单纯增加 YAML 字段。

核心规则：**显式请求优先；省略时采用目标 Agent 的默认值；Agent 没配置时采用平台固定兜底 `ask_for_approval`。子 Agent 的默认值不受父 Run 档位裁剪。**

这个设计意味着：管理员给一个公开 Agent 配置 `full_access`，就授权了能够调用它的 Agent 在省略档位时启动高档位工作。即使父 Run 只有 `ask_for_approval`，结果也一样。这是已提出需求的直接语义，不能同时宣称“父 Run 降档约束整个调用链”或“子任务绝不高于父任务”。

交付必须同时包含：

1. 配置解析、管理读写、冻结版本与所有相关启动入口。
2. `catalog_manage.apply` 修改 accessConfig 的强制人工审批。
3. 子执行器独立的权限控制状态，避免被父 RunControl 覆盖。
4. WebClient 对“跟随 Agent 默认”和“用户显式选择”的区分。
5. Automation 工具现有显式越级路径的收紧，以及 JSONL 审计与兼容规则。

本期不增加全局默认、全局上限、Agent cap、运行中切档自动保存、宿主机任意代码隔离，也不扩展现有调用资格与工具挂载规则。宿主机安全边界见第 8 节。

## 2. 已核实的现状及原稿修正

| 结论 | 当前事实与证据 |
|---|---|
| 缺少 Agent 默认档位 | `internal/catalog/registry.go` 的 AgentDefinition 没有 accessConfig；`internal/contracts/interfaces.go` 的 NormalizeAccessLevel 将空值转成 default。 |
| admission 过早丢失省略语义 | `internal/runtime/query/admission.go` 在 AcquireRun 之前归一化 req.AccessLevel。应移动最终解析，而非只在 Session builder 再兜底一次。 |
| chat_start 当前继承父档位 | `internal/contracts/run_start.go` 的 ResolveRunStartAccessLevel；`internal/runtime/query/chat_tools_facade.go` 在父权限临界区再次确认。显式越级当前支持一次性人工审批。 |
| Mailbox 是独立受理链路 | `internal/runtime/query/mailbox.go` 的 AcceptMail 再次调用上述解析；DeliverMail 的新 Run 使用邮件已冻结的 AccessLevel。只改 chat_start 不够。 |
| 子任务实现目录已变化 | 当前是 `internal/runtime/orchestration/coordinator.go`、`internal/runtime/session/session_builder.go`，原稿中无 runtime 前缀的路径已过时。 |
| 仅改子 Session 会失效 | Coordinator 的请求和 Team 控制器都取父 Session 档位；没有提供子控制器时 childRunCtx 沿用父上下文。`internal/llm/run_stream_access_level.go` 会从 RunControl 反写 session/execCtx。 |
| side query 不是服务端自动继承父 Run | `internal/runtime/query/side_query.go` 直接归一化输入档位，没有读取父 Run 当前档位；省略时为 ask_for_approval。原稿“继承所在 Run”的说法不准确，客户端传入值不能等同于服务端继承。 |
| 计划续接还有独立兜底 | `internal/agent/planmode/continuation.go` 的两个请求构造器把空档位补成 default；批准执行不能只从原始 query 推断本次有效档位。 |
| apply 的自动审批确实存在 | `internal/platformcontrol/control_handler.go` 对 apply 设置 AllowAutoApprove；`internal/llm/run_stream_tool_approval.go` 在 auto_approve/full_access 下自动消费精确审批。 |
| isProject 不构成独立旁路 | `internal/adminsource/control.go` 的 Prepare 同时处理 IsProject，并要求它只用于 Agent 主定义 apply；应在这个统一事务内提取权限差异。 |
| 工具导入当前不支持 Agent | `internal/platformcontrol/control_validation.go` 的 import 仅允许 Skill/Connector；管理端 Agent 导入仍需使用相同配置校验。 |
| protected 路径同时禁读禁写 | `internal/accesspolicy/accesspolicy.go` 在区分 ReadAccess/WriteAccess 前检查 ProtectedPaths。因此不应直接把 AgentsDir 加进现有 secrets 保护集合。 |
| @agent 指向运行快照 | `internal/runtime/session/session_builder.go` 将 LocalPaths.AgentDir 设为 agentDef.RuntimeDir。不能把 @agent 的只读规则误当成对 `agents/<key>` 源码目录的保护。 |
| 档位是固定枚举 | 虽然 tools.yml 的 levels 使用 map，公开契约和排序只识别 ask_for_approval/auto_approve/full_access；有效策略会合并内置默认。不应只校验“键是否存在于原始 tools.yml”。 |
| WebClient 无条件传档位 | `src/features/composer/components/ComposerArea.tsx` 的两处入口在选择器禁用时仍传 default；`composerAccessLevel.ts` 省略状态也兜底 default。 |
| WebClient 还会自动写入选择状态 | `hooks/useMessageActions.ts` 的 initializeChatAccess 将 accessLevel 或 default 写入新 Chat 的存储；只去掉发送端字段，仍会在下一次请求重新出现覆盖。 |
| 普通 Bash 不直接获得身份 token | 仓库约束和现有测试均要求移除普通 Shell 的 AP_ACCESS_TOKEN；验证的 oneid CLI/MCP 可即时获得身份。管理路由只接受有效身份这一点，不足以证明所有连接器进程都无法调用管理接口。 |

原稿识别的自我提权链条成立：自动 apply 新建/修改另一个 Agent 的高档位默认值，再省略档位调用它。禁止 apply 自己无法阻止这条链路。

Automation 的风险也有源码支持：prepareAutomationApproval 对非 delete 允许自动审批，PrepareControl 接收 full_access，执行链没有父档位上限判断。这是**静态调用链结论**；本次没有向真实运行实例创建或触发高权限任务，不将其写成已完成生产环境复现。

## 3. 配置合同与待定项决策

### 3.1 YAML 形状

```yaml
modelConfig:
  modelKey: example-model
accessConfig:
  default: auto_approve
interactionConfig:
  accessLevel: true
```

推荐字段名为 `ask_for_approval`。块名已经给出 access 语境，无需再使用 defaultLevel；对外只有一种拼写，不同时兼容两个新名字。

规则：

- 缺少 accessConfig 或 `accessConfig: {}`：有效默认值为 ask_for_approval。
- 声明 default 时必须是三个标准小写枚举之一。
- 显式 null、空字符串、非字符串、数组，以及整个块为 null/标量，均报配置错误，不静默回落。
- 块内未知字段报错，包括 defaultLevel、cap 和大小写变体。以后实现 cap 再正式开放。
- 顶层其他未知字段的既有处理不在本期扩展；新增块采用严格校验。
- 结构化保存将 accessConfig 排在 modelConfig 后面。缺省值只在加载/解析时提供，不为未配置 Agent 自动写回该块。
- 值对象与 raw definition 分离：raw 保留是否声明；运行时通过统一方法得到有效默认值。
- GENERAL/CODER/KBASE/TEAM/EXECUTOR 共用一个实现，不按 mode 决定默认权限。

### 3.2 原待定项的推荐结论

| 项目 | 推荐决策 | 理由或影响 |
|---|---|---|
| default / defaultLevel | default | 单一配置入口，避免重复语境。 |
| 显式请求是否覆盖 | 覆盖 | 用户可以选择低于或高于 Agent 默认值的档位；Agent 默认值不是上限。 |
| 父 Agent 显式请求超过父当前档位 | 直接拒绝 | 落实“不得超过”的字面要求；不保留旧的人工提档放行分支。父 Run 可由用户先切档，再重新调用。 |
| 子 Agent 默认值超过父档位 | 允许 | 保留已明确的目标行为，不采用 min(parent, child)。审计记录这是目标默认值。 |
| interactionConfig.accessLevel=false | 使用 Agent 默认值 | 它限制用户选择，不应否定管理员配置。显式不同值拒绝；同值可接受。 |
| Automation 省略档位 | 本期继续固定 ask_for_approval | 自动化是持久独立任务，保持现有仓库合同，避免改 Agent 默认值即改变旧任务权限。后续如统一，需要单独设计来源与迁移。 |
| ACP / PROXY / CHANNEL | 本地 Run 同样解析并透传 | 统一入口语义；不将远端桥的实际权限执行效果当作本地保证，需分别联调。 |
| 全局默认 / cap | 本期不引入 | 三个档位的内容已由 tools.yml 控制；cap 是另一个权限治理需求。 |
| 切档是否写回 | 不写回 | 当前 Run、客户端显式偏好、Agent 持久默认是三个不同状态。 |
| “设为默认” | 本期在 Agent 编辑器保存 | 不新增隐式保存接口；如以后在选择器增加快捷操作，应复用版本校验的管理更新。 |
| 审计 | 扩展现有 accessGrant | 不新增 query 输入字段，不把有效值写进原始输入。 |
| YAML 注释 | 保持既有行为并明确提示 | 结构化保存仍重写 YAML；保留注释使用原文编辑。整套 YAML CST 改造另立任务。 |
| Agent 源码保护 | 新增仅写保护，列为同批加固 | 不复用禁读禁写的 secrets 集合，不声称能隔离任意 Host 脚本。 |
| automation_manage 越级 | 同批收紧 | 阻止通过持久任务显式设置高于调用方当前档位的执行权限。 |

用户指示“开始吧”后，实施采用本表推荐决策，包括“显式越级直接拒绝”和“Automation 维持固定 ask_for_approval”；以后如调整需同步权限矩阵与迁移规则。

### 3.3 interactionConfig 的精确定义

用户入口在解析目标 Agent 后，以**原始显式选择**校验 interactionConfig，再生成有效档位：

| 用户输入 | accessLevel=true | accessLevel=false |
|---|---|---|
| 省略 / 兼容的空输入 | 使用 Agent 默认 | 使用 Agent 默认 |
| 显式值等于 Agent 默认 | 使用该值 | 接受该值，语义没有变化 |
| 显式值不同于 Agent 默认 | 使用该值 | interaction_disabled/既有对应错误 |

不能把解析出的 auto_approve/full_access 回传给旧 Validate，再被误判成用户越过了关闭的选择入口。旧 `Validate` 中“永远允许字面 default”的特例需要改为与目标默认值比较。

内部调度继续使用既有受信任的 interaction 豁免，不改变 Run 冻结的 interactionConfig，也不跳过模型、工具、引用和身份授权。

## 4. 权限解析与入口矩阵

### 4.1 统一解析函数

在中立 contracts 中定义权限值/来源和纯解析函数；catalog 只解析配置，runtime 决定入口行为。

```text
ResolveInitialAccess(requested, agentDefault):
    requested 非空 → 校验，返回 requested + explicit
    agentDefault 已声明 → 返回 agentDefault + agent_default
    否则 → 返回 default + platform_default

AuthorizeAgentExplicitStart(requested, parentCurrent):
    requested 非空且 rank(requested) > rank(parentCurrent) → 拒绝
    其他情况 → 通过
```

父档位只参与 Agent **显式选择**的授权，不参与目标默认值计算。不要把两者合并成带隐式继承的 resolver。

输入兼容继续沿用当前 API 对空字符串的处理；“省略”与字面 `ask_for_approval` 必须始终可区分。原始请求由 OriginalQuery 保存。若未来要拒绝空字符串，单独修改 HTTP/WS 输入合同。

已冻结的内部权限通过不参与 JSON 解码的类型字段传递，标记来源和关联版本；外部调用方不能通过 accessGrant、origin.role 或布尔开关伪造受信任授权。

### 4.2 所有入口的目标语义

| 入口 | 未显式指定 | 显式指定 / 已冻结值 | 快照时点 |
|---|---|---|---|
| HTTP/WS 普通 query，新 Chat/续聊 | 目标 Agent 默认 | 用户请求优先，遵守 interaction | 准入 AcquireRun 获得的版本 |
| POST /api/execute | 目标 Agent 默认 | 用户请求优先，遵守对应入口校验 | 同普通准入 |
| BTW/explain | 目标 Agent 默认 | 用户请求优先；只读工具策略继续生效 | side query 获取的 Agent 快照 |
| chat_start | 目标 Agent 默认 | 显式值不得超过父当前档位 | 实际准入的目标版本 + 父授权受理点 |
| chat_send 的 letter，新开或闲时续聊 | 收件 Agent 默认 | 同 chat_start | 邮件受理时冻结；投递时不重算 |
| chat_send 投递到活动 Run（inserted） | 不创建新权限 | 不切换收件 Run 的当前档位 | 使用活动 Run 本身的状态 |
| reply/notify/ask 触发新 Run | 新 Run 的目标 Agent 默认 | 有内部冻结授权时直接使用；不借用发信方权限 | 邮件受理；保持各类消息既有控制权边界 |
| chat_steer / 活动 Run 控制 | 不重新解析默认 | 沿用目标 Run 当前档位 | 既有 RunControl；本期不新增档位比较 |
| agent_invoke | 被调用 Agent 默认 | 本期不为任务条目新增 accessLevel 参数 | 子任务实际获取的 Agent 版本 |
| Team agent_delegate | 成员 Agent 默认 | 不新增参数 | 根 Run 已冻结的成员定义 |
| 同一 Run 内续接、压缩 | 保留该执行器当前有效值 | 不重读默认 | 原 Run 的控制状态 |
| 计划批准后的新执行 Run | 当前 Agent 已发布默认 | 原始用户显式值仍优先；见 6.4 | 批准时当前发布版本 |
| Automation 定时触发、HTTP trigger | 固定 ask_for_approval | 保存的 query.accessLevel 优先 | 本次任务定义快照 |
| automation_manage create/update/启用/trigger | 固定 ask_for_approval | 需要检查执行档位与调用方当前档位 | 任务 revision + 调用方受理临界区 |
| ACP/Proxy 转发 | 使用前述本地已解析值 | 下游不能因为省略而再次选默认 | 本地 Run 冻结后透传 |

这里的“当前发布默认”不等于每次工具调用重新读取文件；运行中的 Agent 仍使用租约绑定的快照。

### 4.3 必须明确的边界案例

- 父 default、目标默认 full_access、省略档位：允许，来源 agent_default。
- 父 default、目标默认 full_access、显式 full_access：拒绝，来源是 Agent 主动指定。
- 父 full_access、目标没有配置、省略档位：子执行器以 default 启动。
- 父 full_access、目标默认 default、显式 auto_approve：允许。
- 用户显式 default、Agent 默认 full_access：用户 Run 使用 default；之后省略档位调用目标仍可能启动 full_access。
- 调用自己同样使用目标默认语义；模型配置的现有继承规则不随之改变。
- 父 Run 随后降档，不追溯改变已受理的独立子 Run、已受理邮件或成员执行器。
- 这些行为是权限模型的一部分。若以后要“一键降档整个调用链”，需要单独的权限边界合同，不能通过当前字段悄悄实现。

## 5. accessConfig 修改的强制人工审批

### 5.1 差异由事务服务计算

在 `internal/adminsource` 的 Prepare 中，对**已解析出的主定义文件**计算 AccessConfigChange，返回给 platformcontrol。不要在 handler 里对未经归一化的 args.path 做字符串判断，也不在 Server 层加业务解析。

建议的事务元数据：

```text
AccessConfigChange:
    changed
    beforePresent / afterPresent
    beforeDefault / afterDefault
    beforeEffectiveDefault / afterEffectiveDefault
```

比较来自同一次 Prepare 读取的原始 before 和验证后的候选 after。不要另行读 registry 来代替 before，也不要依赖可能脱敏/格式化过的审批展示字符串。

应强制人工的变化：

- 新建 Agent 时声明 accessConfig，包括 `{}` 或 default。
- 给已有 Agent 新增、删除整个块。
- 增加、删除、修改 default 字段，包括改低权限。
- 即使有效结果相同，块存在性或字段存在性改变仍需人工，满足“新增、修改、删除一律人工”。

只改变缩进、顺序或注释不算语义变化。不涉及该块的 apply 保留现有审批策略。无效 after 先报校验错误，不进入审批。

### 5.2 策略与展示

`catalog_manage.apply` 的 AllowAutoApprove 取值：

```text
Agent 主定义且 AccessConfigChange.changed → false
其他 apply → 保持现有规则
```

- ask_for_approval、auto_approve、full_access 下都必须等待人类。
- Mailbox 请示代批也不能放宽这个限制。现有 DelegateApproval 会重新评估 AllowAutoApprove，应补回归测试。
- 保持禁止调用方 apply 自己以及 SameFile 防护。
- permissionFields 增加 accessConfig；表单直接展示变更前后**有效默认档位**与“必须人工确认”原因。
- 优先扩展现有 platform_control_review。confirmationRules 只选视图，不负责判断差异或决定自动审批。
- 管理端编辑、原文保存、Agent 归档导入属于已授权管理路径，使用相同配置校验，不增加 Agent 工具人工审批。
- 当前 Skill/Connector import 不能导入 Agent；以后开放时，必须复用权限差异规则。

### 5.3 审批与并发

复用精确调用摘要、baseRevision、一次性收据与提交前重新 Prepare：

1. 准备候选、验证、计算权限差异和完整内容摘要。
2. 人工审批期间不持有 Catalog 租约、写锁或暂停 watcher。
3. 执行时在既有源码 mutation 事务内重新检查版本、内容、目标身份和摘要。
4. 候选/基线变化则审批失效，明确返回冲突，不自动改用新候选。
5. 消费一次性收据后执行，已知失败不冒充成功；不确定结果沿用 unknown，不自动重放。

“强制人工”不是给任意 apply 扩大写权限。管理事务仍只写其授权资源；普通文件/Bash 不能获得同等豁免。

## 6. Runtime、快照与幂等

### 6.1 普通准入

按以下顺序组织 `PrepareQueryAdmissionRequest`：

1. 保留原始输入和原始 requestedAccessLevel；早期只验证非空枚举，不补默认。
2. 解析 Chat owner / Agent target，AcquireRun 获得冻结定义与 Team 快照。
3. 校验用户显式选择与 interaction；内部受信任调用按既有豁免执行。
4. 解析本次有效档位并形成受信任 AccessResolution。
5. 执行其他准入、构建 Session、初始化 RunControl、记录 query。

Session builder 消费解析结果；为直接调用 builder 的内部路径统一补齐解析，不允许各模块自行用 NormalizeAccessLevel 重置省略语义。

新增值字段须进入 AgentDefinition 的快照复制与 runtime revision 计算。当前 assembler 会将 resolved definition 纳入摘要，需验证新字段未因 json 忽略或单独 DTO 转换遗漏。Team 的成员快照必须包含该字段。

### 6.2 chat_start 与父权限临界区

当前 PrepareRunStart 只读取 registry，实际 admission 才获取租约，二者之间可能热重载。目标默认值不能在 Prepare 阶段算一次就长期信任。

建议：

- 准备阶段返回目标 RuntimeRevision、默认值与解析结果，供展示和后续一致性检查。
- 正式 admission 获得目标租约后，使用该版本核对结果；变化则返回可重试的目标版本冲突，由调用方重新准备。
- 父 Run 的 `AcceptAtRunAccessLevel` 继续作为授权受理点；显式值在此比较父**当前**档位，不能使用旧 Session 值。
- 显式越级返回建议错误码 `run_start_access_level_exceeded`，executionState 为 not_started；不能通过遗留 RunStartReview 人工收据放行。
- 省略档位不做父子 rank 比较；仍校验父 Run 存活、调用资格、身份、引用和目标版本。
- 授权失败释放目标租约，不创建 Chat/Run，不登记成功收据。

保持 request digest 与受理结果分离：同一 invocation 的重试不能因 catalog 改变而重复创建 Run。受理收据另绑定目标 revision、来源、有效档位、父基线。已有收据直接回放；尚未受理的调用重新准备。

旧显式越级人工审批分支需连同 runops 的 chat_start/chat_send 审批准备、参数说明和测试一起调整。不能只改 resolver，却留下可消费旧收据的执行分支。

### 6.3 Mailbox 冻结

- PrepareMail 确定目标后读取目标默认；AcceptMail 在父权限临界区核验并持久化最终 AccessLevel 与 accessGrant。
- 受理前目标配置变化需要重新准备，不能将较高新值静默写入邮件。
- 已入队邮件即使父 Run 结束、目标 Agent 默认值后来变化，投递仍使用已受理值。
- 邮件权限冻结不等于冻结整棵 Agent 定义；投递仍获取当时可用的目标定义，执行权限使用邮件收据。原稿不能混淆这两个时点。
- 同一 mail 的重试、unknown 和重启人工重投沿用原授权，不重算、不扩权；旧邮件没有来源标记时按历史已保存档位解释。
- inserted 是向已有执行器补消息，不能借邮件中的档位改变其权限。既有 chat_steer 不比较双方档位，本期不扩展该合同。
- reply/notify/ask 仍不授予对方 Run 控制；请示审批仍采用冻结操作环境，强制人工、readonly 和 hard block 不能放宽。

### 6.4 规划与同进程续接

两类情况必须分开：

1. **同一个 Run/执行器续接或压缩**：保持当时有效权限与来源，不读取新默认，不因原始 query 没有 accessLevel 而回落 default。有效权限从运行时/顶层已记录的决定读取。
2. **规划 Run 已结束，批准后启动新执行 Run**：沿用仓库“使用当前发布定义”的合同。原始用户有显式档位则继续使用；原始省略则采用批准时当前 Agent 默认，并在确认处显示本次执行档位。原始省略但用户在规划期间曾主动切档的，以最后一次用户明确选择作为执行覆盖。

为此增加内部 continuation 权限输入，明确保存显式选择意图及最后有效值。计划确认前若默认档位升高，要刷新确认展示；确认绑定的目标版本变化则重新确认，不允许无提示升档。不要用原始 query 的空值或历史旧行推测用户意图。

`internal/agent/planmode` 的请求构造器不能继续自行把空值补为 ask_for_approval；由 Runtime 传入已决定的权限。新执行 Run 的 OriginalQuery 仍保存调用方原样输入，平台决定与 planRunId 写顶层。

Platform 重启仍取消未完成 Run，不新增跨重启恢复能力。旧历史缺少意图信息时保守使用已记录有效档位，并标记为历史来源未知，不重新套用现行默认。

### 6.5 agent_invoke / Team 成员的控制隔离

这是原改动清单中最容易漏掉的实际执行问题。

- 每个执行器创建独立的权限 RunControl/等价权限控制域，初始值从该任务**实际使用的 AgentDefinition**解析。
- Team 使用根 Run 冻结成员的默认值；普通 agent_invoke 使用实际取得租约的被调用定义。不能重新读取磁盘覆盖 Team 快照。
- `subReq.AccessLevel`、`subSession.AccessLevel`、`ExecutionContext.AccessLevel` 和子控制器快照必须一致。
- 不能只修改 NewTeamHITLQueue，因为普通 agent_invoke、未启用 Team 队列时也可能共享父上下文。
- 复用/扩展现有子控制器与 HITL 路由机制，保留父取消、超时、预算与人工提交的路由；隔离的是权限状态，不改变根 Run owner、Chat owner、taskId 和控制归属。
- 用户修改根 Run 档位只影响根执行器；已经启动的成员不联动。不为成员新增公开切档 API。
- 测试必须运行至少一次真实工具权限检查与一次父档位变更，证明 LLM 的 syncAccessLevelFromRunControl 不会把成员档位改回父值。

## 7. Automation 的同批处理

### 7.1 推荐保留当前省略合同

Automation 的权限来自保存的 query.accessLevel，省略固定 ask_for_approval，且不继承 Chat 历史权限。显式在任务里写入 full_access 才能使它以该档位运行。

这是对原待定问题的明确建议：本期不用“Agent 默认值改变所有旧定时任务”的动态语义。普通 admission 新增 Agent 默认后，Automation dispatcher 必须在**内部解析层**补入 default 并标记 automation_default，避免省略被普通准入重新解释。不得为了补内部值而篡改保存的原始 query。

工具创建目前会显式保存 default，继续兼容该行为；HTTP/YAML 原来省略的任务也维持 default，不批量改写源文件。

### 7.2 关闭工具显式越级路径

在调用方中立的 Automation service 授权边界增加可信 caller 基线，platformcontrol 只做转换和审批展示。HTTP 管理路径不伪造 Agent caller。

推荐规则：

- create：最终任务执行档位不得高于调用方当前档位，即使 enabled=false 也一样。
- update：检查合并后的完整定义，而非只检查补丁中的 query.accessLevel。高权限任务的执行消息、目标、cron 或其他内容不能被低档位调用方自动改写。
- setEnabled(true)、trigger：检查任务已有的保存档位；不能因为参数中没有 accessLevel 就跳过。
- setEnabled(false)：允许作为停止动作走既有审批策略。
- delete：继续强制人工审批。
- query/list/get/validate：只读，不增加执行授权。

不高于父当前档位的变更保持既有审批矩阵。超过时直接拒绝；需要更高执行权限，用户从管理端操作或先提升当前 Run 档位。

执行时在 `AcceptAtRunAccessLevel` 与 Automation management 事务一致的受理边界重新检查基线、任务 revision 和完整定义。人工审批期间父 Run 降档也不能继续生效；禁止只在 prepareAutomationApproval 里做一次比较。

应约定锁顺序，例如父权限临界区 → Automation managementMu；回调不得反向获取父控制锁。任务调度和实际模型执行在短事务之后异步进行，不在父权限锁内等待 Run 完成。

已存在的高权限任务不删、不降档；调度器按已保存权限继续执行。低档位 Agent 对它的工具修改/启用/触发受到新规则约束。

## 8. 源码写入、脚本与身份边界

### 8.1 建议采用仅写保护

新增独立的受信任控制源码只写保护集合，在 writeRoots、hostAccess、exact/rule approval 和 full_access 之前检查。命中后直接 block，不允许普通写审批放宽。

最小范围为实际 AgentsDir 与 RUAgentsDir，覆盖主定义、替换目录、删除、移动及其父目录的递归变更。读取继续按原策略执行。相邻 Automation 定义源码也应覆盖，否则同批 Automation 工具限制仍可被结构化文件写直接绕过。

全局 tools/agent-settings 等安全配置、Connector/Skill 源码还存在执行策略影响，需做同类范围审计；不在本方案中无依据地把整个 runtime 根设为只读。只有平台控制事务取得其精确目标的内部写能力，HTTP 管理及外部编辑器仍按各自身份直接修改。

实现可复用现有 trusted readonly roots，但要保证覆盖直接写、路径别名、symlink canonical 路径、目录替换以及删除/移动祖先的分析。只检查 `agents/*/agent.yml` 字符串会留下重命名旁路。

### 8.2 不能承诺完全堵住 Host 脚本

当前 Host Bash 没有 OS 级隔离；自写脚本入口或可信 Skill 入口验证，并不约束其内部任意文件/网络行为。只写保护只能约束平台能识别和检查的操作。

因此：

- 不把“把 AgentsDir 加入 protected”写成修复任意脚本提权的完成条件。
- 不为了本功能删除现有自写脚本/Skill 执行合同；那会改变远大于本需求的能力范围。
- 对不可信 Agent/脚本要求强保证时，需要独立用户、容器只读挂载、受控网络与凭据代理等执行隔离方案，并验证目标部署。
- 本期安全结论限定为“已覆盖受管工具和受管权限配置入口”，保留 Host 任意代码限制说明。

### 8.3 管理接口与连接器身份

普通 Shell 的 token 移除已有实现；仍需针对性验证：

1. restricted connector execution capability 不能调用管理路由。
2. oneid 进程获得的身份 token 在管理路由的实际授权范围，不能仅因 token 是合法身份就认定它没有管理能力。
3. 公开 CLI env、错误输出、进程命令行和工具结果不泄露可复用的管理凭据。
4. 关闭鉴权的部署不能把 Agent 工具审批当作 HTTP 管理安全边界。

不读取或打印真实 token；验证用测试凭据与 fake verifier。若需要区分管理员身份与连接器身份，应作为明确的管理授权改造实施，而非声称 accessConfig 已提供该隔离。

## 9. API、审计和客户端

### 9.1 对外元数据

推荐使用现有 defaultModelKey 类似的展示形状：

- `/api/agents`、`/api/agent` 及对应 WS 返回有效 `defaultAccessLevel`。
- 管理端 raw definition 保留原始 `accessConfig`，并在管理摘要返回有效默认值以便排查。
- 当前 Run 的 accessLevel 仍表示实际档位；不能用 Agent 默认值覆盖活动 Run 状态。
- 新字段只表示默认，不表示 cap。无效 Agent 的管理响应显示配置诊断，不用 default 隐藏错误。
- ACP/Proxy 的运行时有效值由本地 Run 元数据返回，实际桥接支持另行核验。

不要将权限规则塞进 interaction 类型或前端 mode 分支。Server 只映射 DTO 与统一 JSON 包裹。

### 9.2 复用 accessGrant

新写入的 query 行顶层可采用：

```json
{
  "accessLevel": "full_access",
  "accessGrant": {
    "source": "agent_default",
    "accessLevel": "full_access",
    "agentKey": "target-agent",
    "agentRevision": "<frozen revision>",
    "parentAccessLevel": "default",
    "parentAccessVersion": 3
  },
  "query": {
    "agentKey": "target-agent",
    "message": "原始调用内容"
  }
}
```

推荐来源：`explicit`、`agent_default`、`platform_default`、`automation_config`、`automation_default`、`continuation`。历史 `inherited`/`approved` 继续可读，不改写旧行。存在原始显式值时，requestedAccessLevel 仅作为决定元数据记录。

- requestDigest 继续识别调用输入，区分省略与显式 default。
- accessGrant 的来源、目标 revision、有效值写入受理收据；客户端不能提交该结构覆盖权限决定。
- Team/agent_invoke 的任务行记录自身有效档位、来源、taskAgentKey；不要把根 Run 的档位当成员档位。
- Mailbox 入队授权、投递 Run、人工重投能够关联原始 mailId 与冻结授权。
- 运行中切档沿用既有事件，保留初始来源；活动状态返回当前值。
- 原始 query 不补字段，不变更 role/origin 规则，不按当前配置重算历史权限。

### 9.3 WebClient

将客户端选择状态建模为两种意图：

```text
agent_default → 请求省略 accessLevel
explicit(level) → 请求发送 accessLevel，即使它恰好等于 Agent 默认
```

具体要求：

- UI 提供“跟随 Agent 默认（当前：xxx）”与三个显式档位；“跟随默认”不是第四种 API 枚举。
- 展示值由 defaultAccessLevel 或活动 Run 当前值计算；不能将展示值直接作为请求值。
- 禁用 interactionConfig.accessLevel 时隐藏/禁用选择，始终省略字段。
- 修改 ComposerArea 两处发送入口、ComposerAccessState/reducer、useMessageActions 的自动初始化，以及 shared data 的可选参数类型。
- 新 Chat 创建后复制的是用户选择意图；服务端 run.start 回传的有效值只更新展示，不自动变成 explicit。
- sessionStorage 使用新版本。旧 v1 无法区分初始化值和用户选择，推荐清空旧权限偏好，避免自动恢复的 full_access 覆盖 Agent 默认；这只影响客户端临时偏好，不修改历史 Run。
- 新版状态中的显式选择保留账户/连接/Agent/Chat 隔离，并提供“跟随默认”清除覆盖。
- 正在运行时切档成功才提交相应偏好；失败回滚。若切回“跟随默认”，先请求当前已展示的 Agent 默认档位更新 Run，成功后保存跟随意图；以后新 Run 继续省略。服务端仍不写 YAML。
- Agent 编辑器增加默认档位配置及清除操作；未配置状态保留为空，不在无关保存时补默认。
- “重置”只重置对应区域，不能误清 accessConfig；仅模型保存保留该块。

### 9.4 Desktop

继续在未设置档位时省略字段，同时核查：

- run-controller 的两处请求构造。
- 看板 run-policy、HTTP/WS broker 与嵌入 WebClient 的上游参数。
- Agent 使用目录及编辑器类型新增 defaultAccessLevel。
- 没有显式偏好时，展示 Agent 默认；活动 Run 展示实际档位。
- ACP 与远程 Proxy 的档位映射采用联调用例验证，桥不支持时明确报告，不能静默声称 full_access 已生效。

## 10. 实施拆分与代码责任

| 阶段 | 主要位置 | 产物和完成条件 |
|---|---|---|
| A. 合同与配置 | contracts；catalog/agent_type、agent_loader、registry、agent_editor；adminsource；管理 DTO/映射 | 严格配置解析、缺省不落盘、序列化顺序、所有保存/创建/导入路径统一验证；新字段参与快照。 |
| B. 审批与加固 | adminsource/control；platformcontrol/control_handler；AccessPolicy 受信任只写根；Automation service/handler | 权限块变化强制人工，禁止代批放宽；Automation 显式越级禁止；直接受管源码写保护。 |
| C. 普通与独立 Run | runtime/query/admission、chat_tools_facade、mailbox、side_query；runtime/session；runops | 统一默认解析、受理原子性、目标版本一致、邮件授权冻结与错误语义。 |
| D. 子执行器与计划 | runtime/orchestration；runtime/query/awaiting_continuation；agent/planmode | 子权限控制域独立；Team 使用冻结成员；同 Run 续接保留权限；新计划执行按明确规则解析。 |
| E. 展示与审计 | api、conversation/chat 映射、query recorder；WebClient；Desktop | 原始输入保留，来源可追踪，客户端不再隐式覆盖默认。 |
| F. 验证与文档 | 各模块测试；docs、AGENTS、发布说明 | 关键矩阵、并发、历史、跨客户端与桥联调全部有结果；未验证项明确记录。 |

B 与 C/D 必须同批启用。不能先上线“默认值不受父权限限制”，再补权限配置人工审批。

Server 层不实现 YAML 差异、父子权限决策或 Automation 业务授权。共享逻辑放在对应中立模块，不增加 mode switch，不引入新框架、数据库或持久权限审批库。

## 11. 验收矩阵

### 11.1 配置与保存

- 缺省、空对象、三个合法值；null/空串/错误类型/未知字段/大小写错误均按合同处理。
- 原文保存、结构化保存、创建、项目创建、归档导入、目录 watcher 使用同一校验。
- 仅保存 modelConfig、连接器挂载变更、编辑器重置不丢配置；modelConfig 后的顺序稳定。
- accessConfig 改变后发布新 revision；活动 Run 与 Team 冻结版本不变。
- 无效热重载遵循最近成功版本/诊断合同，不静默发布 default。

### 11.2 所有启动入口

- 三个目标默认 × 省略/三个显式值；HTTP/WS/execute 的同等语义。
- 三个父当前档位 × 三个目标默认 × 省略/三个显式值；验证越级显式拒绝与高默认允许。
- interaction=false 下省略、同值、不同值；新旧客户端兼容错误可理解。
- 子 Agent 无配置时不再继承父 full_access；调用自己也遵循目标默认。
- BTW/explain 默认解析与只读 ToolExecutionPolicy 同时生效。
- Team 成员和普通 agent_invoke 各自运行真实工具权限检查，父切档不会覆盖成员控制状态。
- 父取消/预算耗尽仍影响子执行生命周期，HITL 提交路由正常。

### 11.3 审批与攻击链

- auto_approve/full_access 调用方：新增/删除块、修改/删除 default、新建带块 Agent 均强制人工。
- `{}` 与缺省、显式 default 与缺省的结构变化也要人工；纯格式变化不触发特殊限制。
- 普通不变更权限的 apply 保持既有自动审批。
- 调用方不能修改自己，路径别名和 SameFile 不绕过。
- isProject 与普通创建使用同一保护；未知路径不能误识别成非主定义。
- “改别人为 full_access，再省略档位启动”的第一步被人工审批拦截。
- Mailbox 请示上级 full_access 也不能代批强制人工的权限配置变更。
- 人工审批期间候选、目标路径、baseRevision 改变，旧收据无效；收据不可复用到兄弟 toolId。
- 普通 file_write/file_edit、可分析 Bash 的直接写/删除/rename、symlink 和祖先目录变更命中只写保护；读权限不因新增保护意外消失。
- 任意 Host 脚本内部访问不作为本期已隔离能力验收，单独记载边界。

### 11.4 Mailbox、并发与计划

- 邮件受理后改目标默认值、结束父 Run，再投递，仍用原冻结权限。
- inserted 不改收件 Run 权限；started/inserted 的控制权边界不变。
- 相同调用重试/unknown 不重复创建；重启人工重投不重算权限。
- 目标默认在 Prepare/admission 间变化，失败并释放租约；不出现 UI 预览一个档位、执行另一个。
- 父 Run 在显式启动/Automation 管理 prepare 后降档，正式受理重新比较并拒绝。
- 压缩及同 Run 续接保留当前值；计划批准后按 6.4 使用当前版本且确认展示正确。
- 旧历史没有来源字段时能读，不推断或重算；Platform 重启不恢复未完成 Run。

### 11.5 Automation 与客户端

- HTTP/YAML/工具创建的 Automation 省略档位均仍为 ask_for_approval，不受 Agent 默认改变影响。
- auto_approve 创建 disabled/full_access 任务也拒绝；更新补丁省略 accessLevel、启用、trigger 高档位任务仍不能绕过。
- 停用已有高档位任务保持可操作；delete 仍人工。
- JSONL 原始 query 没被内部 default 补值污染。
- WebClient 未选择、自动初始化、刷新、切 Agent、建新 Chat 均不发送隐式 default。
- 用户显式 default 必须发送；跟随默认时不发送；v1 存储迁移不会恢复高权限覆盖。
- Desktop 普通聊天、看板、嵌入 WebClient、ACP/Proxy 分别联调。

推荐执行顺序：针对模块测试 → 涉及并发的 `go test -race` → `go test ./...` / `make test` → WebClient/Desktop 类型与针对性测试 → 目标桥/运行环境联调。新行为验收不能用本次旧行为基线测试替代。

## 12. 发布、兼容与回滚

1. 先交付权限配置审批及 Automation 加固，再在同一正式版本启用新默认行为；开发分支可按阶段提交，但不单独发布危险中间态。
2. WebClient 先具备省略/显式状态和禁用选择器时省略字段的能力，再配置高档位 Agent 默认；旧服务端收到省略仍回落 default。
3. 老客户端仍显式发 default 时，不能靠服务端猜测是否用户选择；明确记录它将覆盖新默认。interaction=false 且默认非 default 时可能被新版严格校验拒绝，应随客户端升级消除。
4. 发布前盘点所有 Agent accessConfig、TEAM 成员、常用子调用和 Automation。旧 Agent 无需批量写配置；默认行为从“继承父档位”变为“default”本身也是兼容变化。
5. Agent 默认升高时，UI 与发布说明说明其可被低档位调用者按默认启动，用户降低父 Run 档位不限制整条调用链。
6. 旧二进制会忽略未知 accessConfig，同时可能恢复 apply 自动审批与父权限继承；不能宣称直接回滚二进制保持等价安全语义。推荐前向修复。确需回退时，先停止接收新调用、备份定义并清理/另存新配置，再评估旧入口权限行为，处理在途邮件与任务后回退。
7. 不修改真实 .env/configs/token，不提交 runtime 数据。文档与计划不把未实现项写成已交付。

实现时同步更新：

- `docs/智能体配置说明.md`、`docs/Agent配置合并.md`：配置、缺省、保存和冻结行为。
- `docs/子智能体调度.md`、`docs/Mailbox.md`：默认来源、显式越级、入队冻结与控制权。
- `docs/API与协议.md`、`docs/智能体执行接口.md`：省略语义、默认元数据、错误及 JSONL 来源。
- `docs/Platform控制工具设计.md`：权限配置强制人工与差异展示。
- `docs/自动化.md`：保持省略 default，以及工具调用方的执行档位限制。
- `docs/工具目录权限.md`、`docs/AccessPolicy与HITL改造.md`：只写保护、Host 脚本边界、切档不写回。
- `docs/手工测试用例.md`、`docs/README.md`、`AGENTS.md` 与发布说明：验证入口及当前事实。
- 检查 README/.gitignore 是否需要变更；仅新增配置能力通常无需改忽略规则，不做无关目录调整。

## 13. 实施前评估阶段的验证记录

以下是最初仅修改方案文档时的验证记录，实施结果见下一节。

已完成源码交叉检查：Platform、相邻 WebClient 与 Desktop；没有读取真实身份内容、变更部署配置或向真实服务发起高权限任务。

已运行以下旧行为基线用例，四个包均通过：

```bash
go test ./internal/contracts ./internal/platformcontrol ./internal/automation ./internal/llm \
  -run 'Test(ResolveRunStartAccessLevel|RunStartDigestsSeparateIdempotencyFromApproval|AcceptAtAccessLevelExcludesUpdatesAndEndedRuns|ControlAdmissionAndExactApproval|ControlProjectValidationAndPublication|AutomationToolAdmissionApprovalAndPolicy|ControlCreateReplayUpdateAndDelete|DelegatedApprovalRejectsMandatoryHumanReview|DelegatedToolApprovalReevaluatesLiveLevel)$' \
  -count=1
```

这些测试核实当前继承、审批、项目创建、Automation 管理与强制人工不可代批的基础机制；它们不证明新 accessConfig 已实现，也不替代新增行为矩阵、真实攻击链和 ACP/Proxy 目标环境验证。


## 14. 实施记录（2026-10-29）

Platform 已接入严格 accessConfig.default 解析、缺省不写回、YAML 排序和默认元数据；普通/side/execute 准入、独立启动、Mailbox 受理及子执行器统一解析目标默认。子执行器持有独立权限 RunControl，Team 使用冻结定义，任务行记录自己的权限来源。父 Agent 显式越级直接拒绝，目标准备与实际租约不一致时拒绝受理。

adminsource 在同一次准备事务中比较原始权限块，新增/修改/删除（含空块、降档）强制人工，并支持修复原本无效的权限配置；审批视图显示有效档位差异。受管 Agent/Automation 源码与 Agent 运行目录增加只写保护。Automation 的创建、完整更新、启用及触发在调用 Run 权限临界区校验执行档位，已完成调用的收据回放不重复执行也不因后来降档改变结果。

规划批准在消费答案前冻结目标和权限；默认升高时，需 param.executionAccessLevel 明确确认，错误保留等待项。WebClient 计划界面刷新默认值、展示并传递确认值；同 Run 续接保留有效权限，原始显式选择与规划期间用户切档优先。

相邻 WebClient 已增加跟随 Agent 默认选项、v2 显式偏好、独立运行态展示、编辑器默认字段及清除操作。未选择时不发送 accessLevel，run.start/回放不生成显式覆盖；切档失败恢复先前选择意图。Desktop 的未设置字段省略行为无需修改，相关桥接测试通过。

全量测试期间发现 Automation 历史库关闭与后台 ListRunning 的既有竞态：数据库指针在加锁前检查、关闭后被置空，可能空指针崩溃。已将该存储的状态检查放进同一互斥区，并补充并发查询/重复关闭回归。

已验证：默认值与显式值、低父档位调用高默认、权限块强制人工、Automation 禁止显式越级和降档后幂等、Team/普通子执行器实际路径权限、父切档隔离、延迟邮件保留授权、计划权限升级预检、源码 Bash 移动/删除/替换保护。客户端相关 12 组 272 个测试及类型检查通过，五个权限相关后端包的针对性 race 检查通过。

限制保持明确：未启动或重启用户部署，未修改真实 Agent/Automation 定义，未做真实外部 ACP/Proxy 桥权限联调，未将 Host 任意脚本隔离或管理身份边界审计宣称为新增保证。源码受管工具保护不约束所有 Host 代码内部行为。


最终验证记录：

- `go test -p 1 ./...` 全部通过；Server 独立全包复验通过；`CGO_ENABLED=0 go build` 主程序构建通过，产物仅放临时目录。
- Runtime Query、Automation、Platform Control、AccessPolicy、Server 的权限定向 race 检查通过；Automation 全包 `-race` 通过，包括历史库并发查询/关闭。
- WebClient `tsc --noEmit`、i18n 检查、44 个 feature 的模块边界检查及 Desktop 桥协议镜像检查通过。
- WebClient 本次相关 12 组、272 个测试通过。全量 500 组中 496 组通过，4282 个用例中 4250 通过、32 失败；其中 30 个 EventPopover/TopNav 语言文案断言已在 HEAD 原始源码归档单独复现，另外 2 个是同时调整的 detail 协议相关断言（eventDetailLevel、generated Agent WebClient bridge mirror），不将前端全量写成通过。
- Desktop 现有显式 accessLevel 透传定向用例通过；未设置时省略字段的实现保持现状。
- 两个仓库的 `git diff --check` 通过。共享工作区中的其他改动保持原状，未提交或部署本次功能。


## 默认选择器改造与验证

本轮将请求选择器与实际执行权限分开：`default` 或省略表示由后台解析目标 Agent 的 `accessConfig.default`；目标未配置时使用 `ask_for_approval`。具体执行档位为 `ask_for_approval`（Ask for approval）、`auto_approve`（Auto-approve）和 `full_access`（Full access）。Runtime、顶层 JSONL 决策及权限事件只保存具体值，原始 query 保留调用方的 default/省略。

Composer 和 Execute 测试面板只显示三个具体档位，删除“跟随 Agent 默认”入口。未明确选择时请求发送 default，按钮依据后台默认元数据展示；Run 启动后显示冻结的实际权限，运行观察不转成显式偏好，用户明确选择后发送具体值。编辑器的默认配置与本次请求权限分离，运行切档不保存 Agent 源码。

兼容范围：旧顶层 JSONL default 仍按已冻结的请求审批决定读取，不查询当前 Agent 配置；旧规划原始输入中的显式 default（含大小写与空格变体）续接时保留原来的请求审批。旧 Agent 配置 accessConfig.default: default、旧 tools 策略 levels.default/inherit 在内存读取时映射到 ask_for_approval；旧前端 v2 显式 default 偏好迁至 v3 的 ask_for_approval。没有改写部署配置、历史 JSONL 或真实凭据。新请求中的 default 使用新契约，相关消费者应同步升级。

Automation 保持原来的场景默认：default/省略均请求审批；Gateway 和 Desktop WebApp execute 的固定低权限路径明确发送 ask_for_approval。Desktop bridge、Kanban 参数校验接受新的具体值。Codex/Claude ACP bridge 接受 Platform 已解析的 ask_for_approval 并映射为各自交互审批策略；直接 bridge 老调用方的 default 仍只表示旧审批语义，bridge 不负责读取 Agent 默认配置。

改动覆盖 agent-platform、agent-webclient、zenmind-desktop、agent-gateway、codex-acp-bridge、claude-acp-bridge。相邻仓库和本仓库原有的无关工作区改动保留。

验证结果：

- Platform：`go test -p 2 ./...`、`go vet ./...` 与 `CGO_ENABLED=0` 主程序构建通过；补充历史大小写变体测试后 Runtime 权限定向测试通过。contracts/query/session 三个包的 race 测试通过。
- WebClient：typecheck、i18n、feature boundaries、Desktop contract mirror 检查和生产构建通过；相关 14 套、253 个用例通过。全量 503 套中 499 套通过，4297 个用例通过；剩余 4 套、32 个失败在未改动的 HEAD 源码副本中逐项复现，失败集合完全一致，涉及既有界面语言、生成契约 hash 断言及旧 detail=none 断言。
- Desktop：主进程编译、renderer 类型检查与架构检查通过；assistant bridge（包含 default 与三个具体档位透传）、WebApp execute 和 Kanban 共 117 个用例通过。
- Gateway：make fmt/test/vet/build 通过，新增固定具体审批档位测试，防止采用目标 Agent 的高权限默认。
- Codex/Claude ACP bridge：各自全量测试、go vet 与 CGO_ENABLED=0 构建通过，新增请求审批策略映射用例。
- 修改文件格式与 git diff --check 通过。测试阶段未重启运行服务或发布远端产物；后续提交与推送结果以 Git 记录为准。ACP 检查使用本地测试后端，不代表真实账号的 Codex/Claude CLI 业务验收。

提交前补验：按任务范围拆分暂存后，分别导出 Platform 与 Gateway 的独立暂存快照，两者全量测试均通过；Platform 的诊断命令和 Gateway 的 detail 透传等其他工作区改动未纳入本轮提交。
