# 指引

本目录是 Agent Platform Runner 的设计事实源（Single Source of Truth）。
进行代码或契约改动前，必须先同步文档。

## 文档规则
1. 代码实现应与 `.doc/` 保持一致；发现偏差先记录 `[DOC-GAP]`，再给出修订方案。
2. 契约改动先改文档，再改代码。
3. 文档变更必须记录到 `.doc/changelog/`。
4. API 基础路径固定为 `/api/ap`。
5. 非 SSE 接口响应壳固定为 `{"code":...,"msg":...,"data":...}`。

## 阅读顺序
1. `.doc/GUIDE.md`（本文件）
2. `.doc/api/SPEC.md`
3. `.doc/api/modules/request.md`
4. `.doc/api/modules/chat.md`
5. `.doc/api/modules/meta.md`
6. `.doc/architecture/DATA_FLOW.md`
7. `.doc/backend/modules/*.md`（实现细节）

## 术语与命名约束
| 术语 | 代码标识 | 含义 | 禁止别名 |
|---|---|---|---|
| Agent | `agent` | 由 JSON 定义驱动的智能体实例 | bot, assistant-profile |
| Agent Mode | `mode` | 运行模式：ONESHOT/REACT/PLAN_EXECUTE | strategy, workflow-type |
| Run | `run` | 一次 query 执行实例，由 `runId` 标识 | session-step |
| Task | `task` | PLAN_EXECUTE 的计划任务项 | subjob, issue |
| Tool (Backend) | `tool` + `kind=backend` | 模型 function calling 调用的后端工具 | function-tool（泛称可用，不能替代 kind） |
| Tool (Frontend) | `tool` + `kind=frontend` | 触发渲染并等待 `/submit` 回填的工具 | widget-tool |
| Tool (Action) | `tool` + `kind=action` | 触发动作，不等待提交 | effect-tool |
| Skill | `skill` | 目录化提示与脚本能力包 | plugin, prompt-pack |
| Viewport | `viewport` | 渲染模板资源（`.html` / `.qlc`） | view-template |
| Chat Image Token | `chatImageToken` / `t` | `/api/ap/data` 细粒度授权令牌 | data-jwt |
| PlanSnapshot | `plan` | 聊天记忆中的计划快照（`planId/tasks`） | planState, taskSnapshot |
| RawMessages | `rawMessages` | `/api/ap/chat?includeRawMessages=true` 返回的原始消息 | transcriptRaw |
| Events | `events` | `/api/ap/chat` 历史事件快照列表 | timeline |
| Reference | `references` | query 输入附带的外部引用元信息 | attachment-meta |

命名约束：
- API 路径统一以 `/api/ap` 开头。
- `chatId` 必须是 UUID。
- 成功响应统一 `code=0,msg=success`。

## ID/KEY 生成实现总表（重点）
| 标识 | 规则 | 实现来源 |
|---|---|---|
| `chatId` | 客户端传入时必须为 UUID；不传由服务端生成 UUID | `AgentQueryService.parseOrGenerateUuid`，`ChatRecordStore.requireValidChatId` |
| `runId` | `epochMillis` 转 base36 | `RunIdGenerator.nextRunId/encodeEpochMillis` |
| `requestId` | 默认等于 `runId`（可显式覆盖） | `AgentQueryService.prepare` |
| `reasoningId` | 实时 SSE：`{runId}_r_{seq}`；记忆落盘缺失时降级为 `r_` + 8 位短 ID | `AgentDeltaToStreamInputMapper.openReasoningBlockIfNeeded`，`ChatWindowMemoryStore.toAssistantReasoningMessage` |
| `contentId` | 实时 SSE：`{runId}_c_{seq}`；记忆落盘缺失时降级为 `c_` + 8 位短 ID | `AgentDeltaToStreamInputMapper.openContentBlockIfNeeded`，`ChatWindowMemoryStore.toAssistantContentMessage` |
| `toolCallId` | 优先使用模型返回 id；缺失时按上下文生成（如 `call_native_*` / `call_*`） | `OrchestratorServices.callModelTurnStreaming`，`ToolExecutionService.executeToolCalls`，`DefinitionDrivenAgent.generateToolCallId` |
| `_toolId` | backend 使用原始 `tool_call_id`；frontend 使用 `t_` + 8 位短 ID | `ChatWindowMemoryStore.createToolIdentity` |
| `_actionId` | `a_` + 8 位短 ID | `ChatWindowMemoryStore.createToolIdentity` |
| `_msgId` | `m_` + 8 位 hex；同一 LLM 回复内 reasoning/content/tool_calls 共享 | `DefinitionDrivenAgent.StepAccumulator.generateMsgId` |
| `agentKey` | 来自 agent 定义 `key`（文件名与配置共同约束），会话可绑定并优先复用 | `AgentDefinitionLoader.tryLoadExternal`，`AgentQueryService.prepare` |
| `skillId` | 取 `skills/<skill-id>/` 目录名并做小写归一 | `SkillRegistryService.normalizeSkillId` |
| `toolName` | capability/tool 名字归一为小写用于索引与匹配 | `CapabilityRegistryService.normalizeName`，`ToolExecutionService.normalizeToolName` |
| `modelKey` | 来自 agent `modelConfig.modelKey`（含 stage 继承），解析时归一并在模型注册中心查找 | `AgentDefinitionLoader.resolvePrimaryModelKey/resolveModelByKey` |

## 禁止行为
- 禁止发明文档未定义的接口、字段、事件名、错误码。
- 禁止更改请求/响应结构而不更新文档。
- 禁止将 `tool.param` 当作 `/submit` 主事件名（主名为 `request.submit`）。

## [DOC-GAP] 处理流程
1. 在冲突文档添加 `[DOC-GAP]`，写明冲突点和代码位置。
2. 提供候选修订方案（改代码或改契约）。
3. 确认后执行修复，并记录到 `.doc/changelog/`。

## 导航索引
| 目标 | 文档 |
|---|---|
| 通用 API 规则与错误规范 | `.doc/api/SPEC.md` |
| 请求发起与回填（query/submit/upload规划） | `.doc/api/modules/request.md` |
| 会话与资产（chat/chats/read/data/viewport） | `.doc/api/modules/chat.md` |
| 元数据接口（agent/skill/tool） | `.doc/api/modules/meta.md` |
| 真流式、多工具、submit 回填、动作边界 | `.doc/architecture/DATA_FLOW.md` |
| 模式、编排、运行时实现 | `.doc/backend/modules/*.md` |

## 覆盖范围
本目录覆盖：
- Agent 编排模式（ONESHOT/REACT/PLAN_EXECUTE）
- REST + SSE 契约
- 工具、技能、模型、视图、记忆与鉴权
- 前端 tool/action 在后端事件流中的协议行为（不再维护前端路由/组件文档）
