# 项目文件树（含代码行数与主要功能）

- 统计范围：`rg --files`
- 文件总数：`294`
- 总行数：`74970`
- 统计日期：`2026-04-23`

说明：代码行数按 `wc -l` 统计；`_test.go` 为测试文件。

## 根目录

- `CLAUDE.md` (357 行): 项目事实源与开发约束说明
- `Dockerfile` (19 行): 容器镜像构建脚本
- `Makefile` (62 行): 常用构建、运行、测试命令入口
- `README.md` (248 行): 项目总览、接口、配置和运行说明
- `VERSION` (1 行): 当前版本号
- `compose.yml` (60 行): 本地/容器编排配置
- `go.mod` (20 行): Go 模块定义与依赖声明
- `go.sum` (27 行): Go 依赖校验和锁定文件

## `cmd/`

- `cmd/agent-platform-runner/main.go` (127 行): 程序入口；初始化 App、启动 HTTP 服务并处理优雅停机
- `cmd/agent-platform-runner/main_test.go` (169 行): 入口与优雅停机流程测试

## `configs/`

- `configs/bash.example.yml` (35 行): Bash 工具与命令执行示例配置
- `configs/container-hub.example.yml` (24 行): Container Hub 沙箱示例配置
- `configs/cors.example.yml` (17 行): 跨域访问示例配置
- `configs/local-public-key.example.pem` (5 行): 本地 JWT 公钥示例文件
- `configs/prompts.example.yml` (14 行): 提示词/文案示例配置

## `docs/`

- `docs/agent-definition-reference.md` (214 行): Agent 定义字段参考
- `docs/configuration-reference.md` (407 行): 配置项总参考
- `docs/manual-test-cases.md` (200 行): 手工测试用例清单
- `docs/memory-configuration.md` (219 行): Memory 子系统配置说明
- `docs/memory-evaluation.md` (297 行): Memory 评测方法与结果说明
- `docs/memory-system-analysis.md` (566 行): Memory 系统现状分析
- `docs/memory-system-design.md` (1006 行): Memory 系统设计文档
- `docs/project-file-tree.md` (415 行): 当前文档；汇总项目文件树、行数与职责
- `docs/sse-event-color-preview.html` (483 行): SSE 事件颜色预览与调试页面
- `docs/versioned-release-bundle.md` (88 行): 版本化发布产物说明

## `internal/`

### `internal/api/`

- `internal/api/types.go` (461 行): HTTP / SSE API 请求响应类型定义

### `internal/app/`

- `internal/app/app.go` (357 行): 应用装配根对象；连接配置、存储、模型、工具、Server 与后台任务
- `internal/app/app_test.go` (36 行): 对应模块的测试与回归验证

### `internal/artifactpusher/`

- `internal/artifactpusher/pusher.go` (265 行): Artifact 推送与通知逻辑

### `internal/bashsec/`

- `internal/bashsec/bash_security.go` (1032 行): Bash 命令安全检查与执行约束

### `internal/catalog/`

- `internal/catalog/agent_loader.go` (534 行): 加载 agent 目录与定义
- `internal/catalog/agent_loader_test.go` (386 行): 对应模块的测试与回归验证
- `internal/catalog/naming.go` (59 行): catalog 命名与路径规则
- `internal/catalog/registry.go` (651 行): catalog 注册表；聚合 agents/teams/skills/tools
- `internal/catalog/registry_test.go` (654 行): 对应模块的测试与回归验证
- `internal/catalog/runtime_loader.go` (24 行): 运行时目录加载辅助
- `internal/catalog/skill_frontmatter.go` (393 行): skill frontmatter 解析
- `internal/catalog/skill_loader.go` (345 行): 加载 skills 目录与定义
- `internal/catalog/team_loader.go` (55 行): 加载 team 目录与定义

### `internal/chat/`

- `internal/chat/artifact_helpers.go` (56 行): 聊天附件/产物辅助方法
- `internal/chat/run_id.go` (47 行): 运行 ID 生成与解析
- `internal/chat/run_id_test.go` (23 行): 对应模块的测试与回归验证
- `internal/chat/search.go` (274 行): 聊天记录搜索
- `internal/chat/search_test.go` (79 行): 对应模块的测试与回归验证
- `internal/chat/snapshot_builder.go` (495 行): 聊天快照构建
- `internal/chat/step_writer.go` (738 行): 步骤事件写入与持久化
- `internal/chat/store.go` (1732 行): 聊天文件存储主实现
- `internal/chat/store_test.go` (2505 行): 对应模块的测试与回归验证
- `internal/chat/types.go` (216 行): 聊天领域模型定义

### `internal/config/`

- `internal/config/config.go` (1001 行): 配置加载、默认值、环境变量与路径解析
- `internal/config/config_test.go` (582 行): 对应模块的测试与回归验证
- `internal/config/simple_yaml.go` (130 行): 简化 YAML 读取辅助
- `internal/config/yaml_tree.go` (436 行): YAML 树解析与访问工具
- `internal/config/yaml_tree_test.go` (132 行): 对应模块的测试与回归验证

### `internal/contracts/`

- `internal/contracts/awaiting_answer.go` (18 行): 等待用户回答状态定义
- `internal/contracts/delta.go` (188 行): 流式 delta 合约定义
- `internal/contracts/errors.go` (61 行): 统一错误类型/错误码
- `internal/contracts/helpers.go` (65 行): contracts 通用辅助函数
- `internal/contracts/interfaces.go` (332 行): 核心接口契约（Agent、Tool、Store、Sink 等）
- `internal/contracts/interfaces_test.go` (37 行): 对应模块的测试与回归验证
- `internal/contracts/notification.go` (13 行): 通知通道契约
- `internal/contracts/policy.go` (106 行): 策略/权限相关契约
- `internal/contracts/prompt_types.go` (140 行): Prompt 结构类型定义
- `internal/contracts/run_control.go` (833 行): 运行控制、生命周期与状态机
- `internal/contracts/run_control_test.go` (239 行): 对应模块的测试与回归验证
- `internal/contracts/stage_settings.go` (107 行): 阶段设置定义
- `internal/contracts/tool_lookup.go` (31 行): 工具查找辅助
- `internal/contracts/tool_names.go` (3 行): 工具名常量
- `internal/contracts/value_helpers.go` (136 行): 通用值处理辅助函数

### `internal/frontendtools/`

- `internal/frontendtools/ask_user_question.go` (276 行): 前端 ask-user 工具实现
- `internal/frontendtools/common.go` (203 行): 前端工具公共数据结构与辅助方法
- `internal/frontendtools/handlers_test.go` (247 行): 对应模块的测试与回归验证
- `internal/frontendtools/registry.go` (54 行): 前端工具注册表

### `internal/hitl/`

- `internal/hitl/checker.go` (9 行): HITL 触发检查入口
- `internal/hitl/command_parser.go` (297 行): 命令解析与 HITL 判定
- `internal/hitl/command_parser_test.go` (103 行): 对应模块的测试与回归验证
- `internal/hitl/interceptor_test.go` (478 行): 对应模块的测试与回归验证
- `internal/hitl/loader.go` (327 行): HITL 规则加载
- `internal/hitl/loader_test.go` (325 行): 对应模块的测试与回归验证
- `internal/hitl/registry.go` (199 行): HITL 规则注册表
- `internal/hitl/skill_checker.go` (191 行): 技能级安全/HITL 检查
- `internal/hitl/skill_checker_test.go` (145 行): 对应模块的测试与回归验证
- `internal/hitl/types.go` (50 行): HITL 类型定义

### `internal/llm/`

- `internal/llm/approval_summary_context.go` (27 行): 审批摘要上下文拼装
- `internal/llm/delta_mapper.go` (385 行): 模型流式输出到平台 delta 的映射
- `internal/llm/delta_mapper_test.go` (213 行): 对应模块的测试与回归验证
- `internal/llm/frontend_submit.go` (149 行): 前端工具提交协调
- `internal/llm/frontend_submit_test.go` (223 行): 对应模块的测试与回归验证
- `internal/llm/helpers.go` (183 行): LLM 模块通用辅助函数
- `internal/llm/hitl_submit.go` (150 行): HITL submit 协调逻辑
- `internal/llm/hitl_submit_test.go` (83 行): 对应模块的测试与回归验证
- `internal/llm/llm_engine.go` (303 行): Agent Engine 主实现
- `internal/llm/logging.go` (120 行): LLM 请求/响应日志
- `internal/llm/logging_test.go` (67 行): 对应模块的测试与回归验证
- `internal/llm/merge_messages_test.go` (217 行): 对应模块的测试与回归验证
- `internal/llm/mode.go` (50 行): 模型运行模式定义
- `internal/llm/multimodal.go` (138 行): 多模态消息处理
- `internal/llm/orchestration.go` (9 行): 编排相关轻量入口定义
- `internal/llm/plan_execute.go` (508 行): plan/execute 代理编排
- `internal/llm/plan_execute_test.go` (57 行): 对应模块的测试与回归验证
- `internal/llm/prompt_builder.go` (557 行): Prompt 构建主逻辑
- `internal/llm/prompt_builder_test.go` (432 行): 对应模块的测试与回归验证
- `internal/llm/protocol.go` (86 行): 上游模型协议抽象
- `internal/llm/protocol_anthropic.go` (322 行): Anthropic 协议适配
- `internal/llm/protocol_config.go` (117 行): 模型协议配置映射
- `internal/llm/protocol_openai.go` (351 行): OpenAI 兼容协议适配
- `internal/llm/protocol_request_compat_test.go` (222 行): 对应模块的测试与回归验证
- `internal/llm/run_stream.go` (2458 行): 主流式运行链路；驱动模型调用、工具循环与事件输出
- `internal/llm/run_stream_test.go` (2074 行): 对应模块的测试与回归验证
- `internal/llm/submit_result_formatter.go` (43 行): submit 结果格式化
- `internal/llm/submit_result_formatter_test.go` (79 行): 对应模块的测试与回归验证

### `internal/mcp/`

- `internal/mcp/availability_gate.go` (100 行): MCP 可用性门控
- `internal/mcp/client.go` (273 行): MCP 客户端实现
- `internal/mcp/client_test.go` (181 行): 对应模块的测试与回归验证
- `internal/mcp/helpers.go` (7 行): MCP 辅助函数
- `internal/mcp/reconnect.go` (47 行): MCP 自动重连循环
- `internal/mcp/registry.go` (323 行): MCP server 注册表
- `internal/mcp/reloader.go` (26 行): MCP 注册表热重载入口
- `internal/mcp/tool_sync.go` (323 行): MCP 工具同步到本地定义
- `internal/mcp/types.go` (148 行): MCP 类型定义
- `internal/mcp/types_test.go` (41 行): 对应模块的测试与回归验证

### `internal/memory/`

- `internal/memory/constants.go` (3 行): Memory 常量
- `internal/memory/context_builder.go` (614 行): Memory 上下文构建
- `internal/memory/embedding.go` (140 行): 向量嵌入 Provider 适配
- `internal/memory/embedding_test.go` (14 行): 对应模块的测试与回归验证
- `internal/memory/extractor.go` (87 行): 从聊天中提取 memory 候选
- `internal/memory/feedback.go` (85 行): Memory 反馈记录
- `internal/memory/journal.go` (89 行): Memory 日志/时间线记录
- `internal/memory/lifecycle.go` (233 行): Memory 生命周期操作
- `internal/memory/logging.go` (52 行): Memory 模块日志辅助
- `internal/memory/safety.go` (113 行): Memory 安全与过滤规则
- `internal/memory/snapshot_renderer.go` (104 行): Memory 快照渲染
- `internal/memory/sqlite_store.go` (1947 行): SQLite Memory Store 主实现
- `internal/memory/store.go` (874 行): 文件 Memory Store 兼容实现与 Store 接口
- `internal/memory/store_test.go` (1723 行): 对应模块的测试与回归验证
- `internal/memory/summarizer.go` (535 行): Memory 摘要器与 remember 合成逻辑
- `internal/memory/tool_records.go` (157 行): 工具级 memory 记录结构与转换
- `internal/memory/types.go` (336 行): Memory 领域类型定义

### `internal/models/`

- `internal/models/model_registry.go` (474 行): 模型/Provider 注册表加载与查询
- `internal/models/model_registry_test.go` (91 行): 对应模块的测试与回归验证
- `internal/models/provider_secret.go` (74 行): Provider 密钥解密与处理
- `internal/models/provider_secret_test.go` (107 行): 对应模块的测试与回归验证

### `internal/observability/`

- `internal/observability/logging.go` (34 行): 可观测性基础日志能力
- `internal/observability/memory_logger.go` (90 行): Memory 专用日志器
- `internal/observability/request_logger.go` (20 行): 请求日志辅助
- `internal/observability/tool_logger.go` (12 行): 工具调用日志辅助

### `internal/reload/`

- `internal/reload/catalog_reloader.go` (300 行): catalog 热重载与文件变更响应
- `internal/reload/catalog_reloader_test.go` (71 行): 对应模块的测试与回归验证

### `internal/resources/`

- `internal/resources/embed.go` (6 行): 嵌入内置资源文件

#### `internal/resources/tools/`

- `internal/resources/tools/agent_invoke.yml` (32 行): 内置工具定义：agentinvoke
- `internal/resources/tools/artifact_publish.yml` (30 行): 内置工具定义：artifactpublish
- `internal/resources/tools/ask_user_question.yml` (81 行): 内置工具定义：askuserquestion
- `internal/resources/tools/bash.yml` (24 行): 内置工具定义：bash
- `internal/resources/tools/bash_sandbox.yml` (29 行): 内置工具定义：bashsandbox
- `internal/resources/tools/datetime.yml` (14 行): 内置工具定义：datetime
- `internal/resources/tools/_memory_consolidate_.yml` (7 行): 内置工具定义：memoryconsolidate
- `internal/resources/tools/_memory_forget_.yml` (17 行): 内置工具定义：memoryforget
- `internal/resources/tools/_memory_promote_.yml` (43 行): 内置工具定义：memorypromote
- `internal/resources/tools/_memory_read_.yml` (21 行): 内置工具定义：memoryread
- `internal/resources/tools/_memory_search_.yml` (20 行): 内置工具定义：memorysearch
- `internal/resources/tools/_memory_timeline_.yml` (17 行): 内置工具定义：memorytimeline
- `internal/resources/tools/_memory_update_.yml` (43 行): 内置工具定义：memoryupdate
- `internal/resources/tools/_memory_write_.yml` (25 行): 内置工具定义：memorywrite
- `internal/resources/tools/_session_search_.yml` (18 行): 内置工具定义：sessionsearch
- `internal/resources/tools/_skill_candidate_list_.yml` (13 行): 内置工具定义：skillcandidatelist
- `internal/resources/tools/_skill_candidate_write_.yml` (29 行): 内置工具定义：skillcandidatewrite
- `internal/resources/tools/plan_add_tasks.yml` (23 行): 内置工具定义：planaddtasks
- `internal/resources/tools/plan_get_tasks.yml` (9 行): 内置工具定义：plangettasks
- `internal/resources/tools/plan_update_task.yml` (26 行): 内置工具定义：planupdatetask

### `internal/runctl/`

- `internal/runctl/runctl.go` (31 行): 运行控制器内存实现

### `internal/sandbox/`

- `internal/sandbox/client.go` (206 行): Container Hub 客户端
- `internal/sandbox/mounts.go` (373 行): 沙箱挂载解析与构建
- `internal/sandbox/service.go` (337 行): 沙箱服务封装

### `internal/schedule/`

- `internal/schedule/dispatcher.go` (120 行): 调度任务分发
- `internal/schedule/dispatcher_test.go` (134 行): 对应模块的测试与回归验证
- `internal/schedule/orchestrator.go` (486 行): 定时任务编排主逻辑
- `internal/schedule/orchestrator_test.go` (751 行): 对应模块的测试与回归验证
- `internal/schedule/registry.go` (746 行): 调度配置注册表与加载
- `internal/schedule/types.go` (88 行): 调度类型定义

### `internal/server/`

- `internal/server/chat_read_state.go` (75 行): 聊天读取状态管理
- `internal/server/frame_orchestrator.go` (407 行): SSE/WS frame 编排
- `internal/server/frame_orchestrator_test.go` (406 行): 对应模块的测试与回归验证
- `internal/server/handler_attach.go` (86 行): 处理上传附件关联请求
- `internal/server/handler_catalog.go` (57 行): 处理 agents/teams/skills/tools catalog API
- `internal/server/handler_chat.go` (185 行): 处理 chat 查询 API
- `internal/server/handler_chat_search.go` (55 行): 处理 chat 搜索 API
- `internal/server/handler_chat_search_test.go` (49 行): 对应模块的测试与回归验证
- `internal/server/handler_memory.go` (131 行): 处理 memory 读写/检索 API
- `internal/server/handler_memory_test.go` (229 行): 对应模块的测试与回归验证
- `internal/server/handler_query.go` (719 行): 处理 /api/query 主入口与流式响应
- `internal/server/handler_query_integration_test.go` (1039 行): 对应模块的测试与回归验证
- `internal/server/handler_query_test.go` (583 行): 对应模块的测试与回归验证
- `internal/server/handler_resource.go` (170 行): 处理资源下载与 ticket 校验
- `internal/server/handler_resource_integration_test.go` (354 行): 对应模块的测试与回归验证
- `internal/server/handler_run_stream_test.go` (67 行): 对应模块的测试与回归验证
- `internal/server/handler_skill_candidates.go` (29 行): 处理 skill candidate API
- `internal/server/memory_learning.go` (156 行): 聊天后置 memory 学习流程
- `internal/server/memory_learning_test.go` (127 行): 对应模块的测试与回归验证
- `internal/server/prompt_context.go` (545 行): 组装查询时的 Prompt 上下文
- `internal/server/prompt_context_test.go` (494 行): 对应模块的测试与回归验证
- `internal/server/proxy_handler.go` (294 行): HTTP 代理桥接处理
- `internal/server/proxy_ws_handler.go` (594 行): WebSocket 代理桥接处理
- `internal/server/run_executor.go` (390 行): Run 执行调度与收口
- `internal/server/run_executor_test.go` (246 行): 对应模块的测试与回归验证
- `internal/server/security.go` (393 行): 鉴权、ticket 与安全辅助
- `internal/server/server.go` (924 行): HTTP Server 主体；路由、鉴权、中间处理与内部调用封装
- `internal/server/server_catalog_auth_test.go` (346 行): 对应模块的测试与回归验证
- `internal/server/server_hitl_test.go` (1982 行): 对应模块的测试与回归验证
- `internal/server/server_http_smoke_test.go` (120 行): 对应模块的测试与回归验证
- `internal/server/server_ws_test.go` (954 行): 对应模块的测试与回归验证
- `internal/server/session_builder.go` (211 行): 会话上下文构建
- `internal/server/shutdown_test.go` (319 行): 对应模块的测试与回归验证
- `internal/server/submit_validation.go` (98 行): submit 参数校验
- `internal/server/test_support_test.go` (782 行): 对应模块的测试与回归验证
- `internal/server/ws_regression_test.go` (399 行): 对应模块的测试与回归验证
- `internal/server/ws_routes.go` (727 行): WebSocket 路由与事件接线

### `internal/skills/`

- `internal/skills/candidate_store.go` (207 行): skill candidate 文件存储
- `internal/skills/candidate_store_test.go` (43 行): 对应模块的测试与回归验证
- `internal/skills/extractor.go` (231 行): 从对话/记忆中提取 skill 候选
- `internal/skills/extractor_test.go` (37 行): 对应模块的测试与回归验证

### `internal/stream/`

- `internal/stream/artifact_test.go` (110 行): 对应模块的测试与回归验证
- `internal/stream/assembler.go` (90 行): 流式事件组装
- `internal/stream/assembler_test.go` (114 行): 对应模块的测试与回归验证
- `internal/stream/dispatcher.go` (1005 行): 流式事件分发主逻辑
- `internal/stream/dispatcher_test.go` (839 行): 对应模块的测试与回归验证
- `internal/stream/event.go` (402 行): 事件数据模型定义
- `internal/stream/event_bus.go` (349 行): 事件总线实现
- `internal/stream/event_bus_test.go` (236 行): 对应模块的测试与回归验证
- `internal/stream/input.go` (264 行): 流式输入结构
- `internal/stream/normalizer.go` (90 行): 流式事件归一化
- `internal/stream/normalizer_test.go` (25 行): 对应模块的测试与回归验证
- `internal/stream/reasoning_label.go` (58 行): reasoning 标签处理
- `internal/stream/reasoning_label_test.go` (35 行): 对应模块的测试与回归验证
- `internal/stream/source_test.go` (196 行): 对应模块的测试与回归验证
- `internal/stream/sse.go` (283 行): SSE 编码与输出
- `internal/stream/sse_test.go` (133 行): 对应模块的测试与回归验证
- `internal/stream/state.go` (67 行): 流式状态对象

### `internal/tools/`

- `internal/tools/bash_yaml_parity_test.go` (90 行): bash 工具 YAML 与实现一致性测试
- `internal/tools/builtin_tool_definitions.go` (86 行): 内置工具定义集合
- `internal/tools/builtin_tool_definitions_test.go` (176 行): 对应模块的测试与回归验证
- `internal/tools/datetime_memory_helpers.go` (366 行): 日期时间与 memory 辅助方法
- `internal/tools/helpers.go` (18 行): 工具模块通用辅助函数
- `internal/tools/lunar_date.go` (185 行): 农历日期计算辅助
- `internal/tools/memory_logging.go` (28 行): 工具调用中的 memory 日志辅助
- `internal/tools/tool_args.go` (87 行): 工具参数解析与校验
- `internal/tools/tool_artifact.go` (284 行): artifact 发布工具实现
- `internal/tools/tool_bash.go` (216 行): bash 执行工具实现
- `internal/tools/toolbashtest.go` (298 行): 对应模块的测试与回归验证
- `internal/tools/tool_datetime.go` (15 行): 日期时间工具实现
- `internal/tools/tool_definition_loader.go` (107 行): 工具定义文件加载
- `internal/tools/tool_executor.go` (173 行): 运行时工具执行器
- `internal/tools/tool_memory.go` (418 行): memory 读写/检索工具实现
- `internal/tools/tool_memory_consolidate.go` (38 行): memory consolidate 工具实现
- `internal/tools/tool_memory_test.go` (456 行): 对应模块的测试与回归验证
- `internal/tools/tool_plan.go` (130 行): plan 任务工具实现
- `internal/tools/tool_registry.go` (144 行): 运行时/MCP/内置工具定义合并与注册
- `internal/tools/tool_router.go` (291 行): 工具路由器；分发到 backend、frontend、MCP
- `internal/tools/tool_sandbox_bash.go` (21 行): 沙箱 bash 工具实现
- `internal/tools/tool_sandboxbashtest.go` (112 行): 对应模块的测试与回归验证
- `internal/tools/tool_session_search.go` (52 行): session 搜索工具实现
- `internal/tools/tool_session_search_test.go` (43 行): 对应模块的测试与回归验证
- `internal/tools/tool_skill_candidate.go` (53 行): skill candidate 工具实现
- `internal/tools/tool_skill_candidate_test.go` (45 行): 对应模块的测试与回归验证

### `internal/viewport/`

- `internal/viewport/registry.go` (75 行): viewport 注册表加载
- `internal/viewport/server_registry.go` (63 行): 远端 viewport server 注册表
- `internal/viewport/service.go` (46 行): viewport 查询服务
- `internal/viewport/service_test.go` (135 行): 对应模块的测试与回归验证
- `internal/viewport/sync.go` (140 行): viewport 数据同步

### `internal/ws/`

- `internal/ws/conn.go` (488 行): WebSocket 连接生命周期与写队列
- `internal/ws/conn_test.go` (189 行): 对应模块的测试与回归验证
- `internal/ws/handler.go` (137 行): WebSocket HTTP 入口处理
- `internal/ws/handler_test.go` (52 行): 对应模块的测试与回归验证
- `internal/ws/hub.go` (62 行): WebSocket Hub 广播中心
- `internal/ws/hub_test.go` (55 行): 对应模块的测试与回归验证
- `internal/ws/protocol.go` (93 行): WebSocket 协议消息定义
- `internal/ws/protocol_test.go` (57 行): 对应模块的测试与回归验证

#### `internal/ws/gatewayclient/`

- `internal/ws/gatewayclient/client.go` (214 行): 网关 WebSocket 客户端
- `internal/ws/gatewayclient/client_test.go` (318 行): 对应模块的测试与回归验证

## `local-cli-acp-relay/`

- `local-cli-acp-relay/README.md` (25 行): 本地 CLI ACP Relay 使用说明
- `local-cli-acp-relay/relay.mjs` (1042 行): 本地 relay 服务实现；桥接 CLI 与平台协议

## `scripts/`

- `scripts/memory-eval.sh` (75 行): Memory 评测脚本
- `scripts/release-common.sh` (297 行): 发布流程通用函数
- `scripts/release-program.sh` (133 行): 程序发布打包脚本
- `scripts/release.sh` (5 行): 发布入口脚本
- `scripts/verify-memory.sh` (118 行): Memory 结果校验脚本

### `scripts/release-assets/program/`

- `scripts/release-assets/program/README.txt` (15 行): 发布包附带说明

#### `scripts/release-assets/program/unix/`

- `scripts/release-assets/program/unix/deploy.sh` (14 行): Unix 发布包部署脚本
- `scripts/release-assets/program/unix/program-common.sh` (139 行): Unix 发布脚本公共逻辑
- `scripts/release-assets/program/unix/start.sh` (27 行): Unix 启动脚本
- `scripts/release-assets/program/unix/stop.sh` (10 行): Unix 停止脚本

#### `scripts/release-assets/program/windows/`

- `scripts/release-assets/program/windows/deploy.ps1` (11 行): Windows 发布包部署脚本
- `scripts/release-assets/program/windows/program-common.ps1` (161 行): Windows 发布脚本公共逻辑
- `scripts/release-assets/program/windows/start.ps1` (18 行): Windows 启动脚本
- `scripts/release-assets/program/windows/stop.ps1` (7 行): Windows 停止脚本
