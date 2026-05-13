# 项目文件树（含代码行数与主要功能）

- 统计范围：`rg --files`，排除 `node_modules/dist/build/coverage/target/vendor/.git`
- 统计日期：`2026-04-28`
- 文件总数：`341`
- 总行数：`94758`
- Go main 代码：`181` 个文件，`49502` 行
- Go test 代码：`97` 个文件，`36288` 行
- 扩展代码口径（Go + mjs + sh + ps1，测试仍按 `*_test.go`）：main `196` 个文件，`52033` 行；test `97` 个文件，`36288` 行

说明：行数按 `wc -l` 等价口径统计；“主要功能”根据目录职责和文件名归纳。

## 根目录

目录作用：项目根配置、构建入口与总体说明。

- `CLAUDE.md` (358 行): 项目协作约束、事实源和开发提示
- `Dockerfile` (19 行): 容器镜像构建定义
- `Makefile` (71 行): 常用构建、测试、运行命令入口
- `README.md` (296 行): 项目总览、运行方式和接口说明
- `VERSION` (1 行): 项目版本号
- `compose.yml` (60 行): 本地容器编排配置
- `go.mod` (23 行): Go 模块与依赖声明
- `go.sum` (29 行): Go 依赖校验和

## `cmd/agent-platform-runner/`

目录作用：服务进程启动入口。

- `cmd/agent-platform-runner/main.go` (127 行): 程序入口与启动流程
- `cmd/agent-platform-runner/main_test.go` (169 行): 测试与回归验证：main

## `configs/`

目录作用：本地运行和示例配置。

- `configs/bash.example.yml` (35 行): 示例配置：bash
- `configs/channels.example.yml` (38 行): 示例配置：channels
- `configs/container-hub.example.yml` (23 行): 示例配置：container-hub
- `configs/cors.example.yml` (17 行): 示例配置：cors
- `configs/local-public-key.example.pem` (5 行): 本地公钥示例
- `configs/prompts.example.yml` (15 行): 示例配置：prompts

## `docs/`

目录作用：架构、配置、发布、测试和设计文档。

- `docs/agent-definition-reference.md` (221 行): 文档：agent definition reference
- `docs/configuration-reference.md` (421 行): 文档：configuration reference
- `docs/manual-test-cases.md` (200 行): 文档：manual test cases
- `docs/memory-configuration.md` (277 行): 文档：memory configuration
- `docs/memory-evaluation.md` (300 行): 文档：memory evaluation
- `docs/memory-system-analysis.md` (566 行): 文档：memory system analysis
- `docs/memory-system-design.md` (1017 行): 文档：memory system design
- `docs/project-file-tree.md` (558 行): 当前文件树、职责和行数统计文档
- `docs/sse-event-color-preview.html` (483 行): HTML 调试/预览页面
- `docs/versioned-release-bundle.md` (88 行): 文档：versioned release bundle
- `docs/记忆系统dialog设计.md` (644 行): 文档：记忆系统dialog设计

## `internal/api/`

目录作用：HTTP/SSE API 数据结构。

- `internal/api/types.go` (537 行): HTTP/SSE API 数据结构：types
- `internal/api/types_memory_console.go` (130 行): HTTP/SSE API 数据结构：types memory console

## `internal/app/`

目录作用：应用装配根对象。

- `internal/app/app.go` (526 行): 应用装配根对象：app
- `internal/app/app_test.go` (36 行): 测试与回归验证：app

## `internal/artifactpusher/`

目录作用：Artifact 推送逻辑。

- `internal/artifactpusher/pusher.go` (263 行): Artifact 推送逻辑：pusher

## `internal/bashast/`

目录作用：Bash AST 解析、遍历和预检查。

- `internal/bashast/embedded.go` (201 行): Bash AST 解析、遍历和预检查：embedded
- `internal/bashast/embedded_test.go` (58 行): 测试与回归验证：embedded
- `internal/bashast/parser.go` (68 行): Bash AST 解析、遍历和预检查：parser
- `internal/bashast/parser_test.go` (157 行): 测试与回归验证：parser
- `internal/bashast/prechecks.go` (124 行): Bash AST 解析、遍历和预检查：prechecks
- `internal/bashast/types.go` (45 行): Bash AST 解析、遍历和预检查：types
- `internal/bashast/walker.go` (512 行): Bash AST 解析、遍历和预检查：walker
- `internal/bashast/walker_test.go` (97 行): 测试与回归验证：walker

## `internal/bashsec/`

目录作用：Bash 命令安全策略检查。

- `internal/bashsec/bash_security.go` (1637 行): Bash 命令安全策略检查：bash security
- `internal/bashsec/bash_security_test.go` (219 行): 测试与回归验证：bash security

## `internal/catalog/`

目录作用：Agent、Skill、Team、Runtime Catalog 加载与注册。

- `internal/catalog/agent_loader.go` (573 行): Agent、Skill、Team、Runtime Catalog 加载与注册：agent loader
- `internal/catalog/agent_loader_test.go` (492 行): 测试与回归验证：agent loader
- `internal/catalog/naming.go` (59 行): Agent、Skill、Team、Runtime Catalog 加载与注册：naming
- `internal/catalog/registry.go` (691 行): Agent、Skill、Team、Runtime Catalog 加载与注册：registry
- `internal/catalog/registry_test.go` (697 行): 测试与回归验证：registry
- `internal/catalog/runtime_loader.go` (24 行): Agent、Skill、Team、Runtime Catalog 加载与注册：runtime loader
- `internal/catalog/skill_frontmatter.go` (393 行): Agent、Skill、Team、Runtime Catalog 加载与注册：skill frontmatter
- `internal/catalog/skill_loader.go` (345 行): Agent、Skill、Team、Runtime Catalog 加载与注册：skill loader
- `internal/catalog/team_loader.go` (55 行): Agent、Skill、Team、Runtime Catalog 加载与注册：team loader

## `internal/channel/`

目录作用：Channel 注册管理。

- `internal/channel/registry.go` (113 行): Channel 注册管理：registry
- `internal/channel/registry_test.go` (103 行): 测试与回归验证：registry

## `internal/chat/`

目录作用：聊天会话、步骤、快照、搜索和存储。

- `internal/chat/artifact_helpers.go` (56 行): 聊天会话、步骤、快照、搜索和存储：artifact helpers
- `internal/chat/run_id.go` (47 行): 聊天会话、步骤、快照、搜索和存储：run id
- `internal/chat/run_id_test.go` (23 行): 测试与回归验证：run id
- `internal/chat/search.go` (362 行): 聊天会话、步骤、快照、搜索和存储：search
- `internal/chat/search_test.go` (129 行): 测试与回归验证：search
- `internal/chat/snapshot_builder.go` (495 行): 聊天会话、步骤、快照、搜索和存储：snapshot builder
- `internal/chat/step_writer.go` (737 行): 聊天会话、步骤、快照、搜索和存储：step writer
- `internal/chat/store.go` (2242 行): 聊天会话、步骤、快照、搜索和存储：store
- `internal/chat/store_test.go` (3247 行): 测试与回归验证：store
- `internal/chat/types.go` (266 行): 聊天会话、步骤、快照、搜索和存储：types

## `internal/config/`

目录作用：配置加载、默认值、环境变量和 YAML 处理。

- `internal/config/config.go` (1429 行): 配置加载、默认值、环境变量和 YAML 处理：config
- `internal/config/config_test.go` (957 行): 测试与回归验证：config
- `internal/config/simple_yaml.go` (130 行): 配置加载、默认值、环境变量和 YAML 处理：simple yaml
- `internal/config/yaml_tree.go` (436 行): 配置加载、默认值、环境变量和 YAML 处理：yaml tree
- `internal/config/yaml_tree_test.go` (132 行): 测试与回归验证：yaml tree

## `internal/contracts/`

目录作用：跨模块接口、事件、状态和策略契约。

- `internal/contracts/awaiting_answer.go` (18 行): 跨模块接口、事件、状态和策略契约：awaiting answer
- `internal/contracts/delta.go` (188 行): 跨模块接口、事件、状态和策略契约：delta
- `internal/contracts/errors.go` (61 行): 跨模块接口、事件、状态和策略契约：errors
- `internal/contracts/helpers.go` (65 行): 跨模块接口、事件、状态和策略契约：helpers
- `internal/contracts/interfaces.go` (335 行): 跨模块接口、事件、状态和策略契约：interfaces
- `internal/contracts/interfaces_test.go` (37 行): 测试与回归验证：interfaces
- `internal/contracts/notification.go` (13 行): 跨模块接口、事件、状态和策略契约：notification
- `internal/contracts/policy.go` (119 行): 跨模块接口、事件、状态和策略契约：policy
- `internal/contracts/policy_test.go` (36 行): 测试与回归验证：policy
- `internal/contracts/prompt_types.go` (140 行): 跨模块接口、事件、状态和策略契约：prompt types
- `internal/contracts/run_control.go` (862 行): 跨模块接口、事件、状态和策略契约：run control
- `internal/contracts/run_control_test.go` (239 行): 测试与回归验证：run control
- `internal/contracts/stage_settings.go` (107 行): 跨模块接口、事件、状态和策略契约：stage settings
- `internal/contracts/tool_lookup.go` (31 行): 跨模块接口、事件、状态和策略契约：tool lookup
- `internal/contracts/tool_names.go` (3 行): 跨模块接口、事件、状态和策略契约：tool names
- `internal/contracts/value_helpers.go` (136 行): 跨模块接口、事件、状态和策略契约：value helpers

## `internal/frontendtools/`

目录作用：前端交互工具定义与处理。

- `internal/frontendtools/ask_user_question.go` (276 行): 前端交互工具定义与处理：ask user question
- `internal/frontendtools/common.go` (203 行): 前端交互工具定义与处理：common
- `internal/frontendtools/handlers_test.go` (247 行): 测试与回归验证：handlers
- `internal/frontendtools/registry.go` (54 行): 前端交互工具定义与处理：registry

## `internal/gateway/`

目录作用：Gateway 注册信息管理。

- `internal/gateway/registry.go` (301 行): Gateway 注册信息管理：registry
- `internal/gateway/registry_test.go` (253 行): 测试与回归验证：registry

## `internal/hitl/`

目录作用：Human-in-the-loop 规则加载、命令解析和拦截。

- `internal/hitl/checker.go` (9 行): Human-in-the-loop 规则加载、命令解析和拦截：checker
- `internal/hitl/command_parser.go` (324 行): Human-in-the-loop 规则加载、命令解析和拦截：command parser
- `internal/hitl/command_parser_test.go` (123 行): 测试与回归验证：command parser
- `internal/hitl/interceptor_test.go` (478 行): 测试与回归验证：interceptor
- `internal/hitl/loader.go` (329 行): Human-in-the-loop 规则加载、命令解析和拦截：loader
- `internal/hitl/loader_test.go` (351 行): 测试与回归验证：loader
- `internal/hitl/registry.go` (199 行): Human-in-the-loop 规则加载、命令解析和拦截：registry
- `internal/hitl/skill_checker.go` (221 行): Human-in-the-loop 规则加载、命令解析和拦截：skill checker
- `internal/hitl/skill_checker_ast_test.go` (42 行): 测试与回归验证：skill checker ast
- `internal/hitl/skill_checker_test.go` (145 行): 测试与回归验证：skill checker
- `internal/hitl/types.go` (52 行): Human-in-the-loop 规则加载、命令解析和拦截：types

## `internal/hitlsubmit/`

目录作用：HITL 提交流程数据规范化。

- `internal/hitlsubmit/normalize.go` (173 行): HITL 提交流程数据规范化：normalize
- `internal/hitlsubmit/normalize_test.go` (124 行): 测试与回归验证：normalize

## `internal/llm/`

目录作用：模型协议适配、Prompt 构建、流式运行和工具循环。

- `internal/llm/approval_summary_context.go` (27 行): 模型协议适配、Prompt 构建、流式运行和工具循环：approval summary context
- `internal/llm/delta_mapper.go` (385 行): 模型协议适配、Prompt 构建、流式运行和工具循环：delta mapper
- `internal/llm/delta_mapper_test.go` (213 行): 测试与回归验证：delta mapper
- `internal/llm/frontend_submit.go` (149 行): 模型协议适配、Prompt 构建、流式运行和工具循环：frontend submit
- `internal/llm/frontend_submit_test.go` (223 行): 测试与回归验证：frontend submit
- `internal/llm/helpers.go` (183 行): 模型协议适配、Prompt 构建、流式运行和工具循环：helpers
- `internal/llm/hitl_submit.go` (15 行): 模型协议适配、Prompt 构建、流式运行和工具循环：hitl submit
- `internal/llm/hitl_submit_test.go` (83 行): 测试与回归验证：hitl submit
- `internal/llm/llm_engine.go` (303 行): 模型协议适配、Prompt 构建、流式运行和工具循环：llm engine
- `internal/llm/logging.go` (120 行): 模型协议适配、Prompt 构建、流式运行和工具循环：logging
- `internal/llm/logging_test.go` (67 行): 测试与回归验证：logging
- `internal/llm/merge_messages_test.go` (292 行): 测试与回归验证：merge messages
- `internal/llm/mode.go` (50 行): 模型协议适配、Prompt 构建、流式运行和工具循环：mode
- `internal/llm/multimodal.go` (132 行): 模型协议适配、Prompt 构建、流式运行和工具循环：multimodal
- `internal/llm/multimodal_test.go` (105 行): 测试与回归验证：multimodal
- `internal/llm/orchestration.go` (9 行): 模型协议适配、Prompt 构建、流式运行和工具循环：orchestration
- `internal/llm/plan_execute.go` (508 行): 模型协议适配、Prompt 构建、流式运行和工具循环：plan execute
- `internal/llm/plan_execute_test.go` (57 行): 测试与回归验证：plan execute
- `internal/llm/prompt_builder.go` (545 行): 模型协议适配、Prompt 构建、流式运行和工具循环：prompt builder
- `internal/llm/prompt_builder_test.go` (486 行): 测试与回归验证：prompt builder
- `internal/llm/protocol.go` (86 行): 模型协议适配、Prompt 构建、流式运行和工具循环：protocol
- `internal/llm/protocol_anthropic.go` (374 行): 模型协议适配、Prompt 构建、流式运行和工具循环：protocol anthropic
- `internal/llm/protocol_config.go` (117 行): 模型协议适配、Prompt 构建、流式运行和工具循环：protocol config
- `internal/llm/protocol_openai.go` (417 行): 模型协议适配、Prompt 构建、流式运行和工具循环：protocol openai
- `internal/llm/protocol_request_compat_test.go` (222 行): 测试与回归验证：protocol request compat
- `internal/llm/run_stream.go` (2751 行): 模型协议适配、Prompt 构建、流式运行和工具循环：run stream
- `internal/llm/run_stream_test.go` (3104 行): 测试与回归验证：run stream
- `internal/llm/submit_result_formatter.go` (43 行): 模型协议适配、Prompt 构建、流式运行和工具循环：submit result formatter
- `internal/llm/submit_result_formatter_test.go` (79 行): 测试与回归验证：submit result formatter

## `internal/mcp/`

目录作用：MCP 客户端、注册、重连和工具同步。

- `internal/mcp/availability_gate.go` (100 行): MCP 客户端、注册、重连和工具同步：availability gate
- `internal/mcp/client.go` (273 行): MCP 客户端、注册、重连和工具同步：client
- `internal/mcp/client_test.go` (181 行): 测试与回归验证：client
- `internal/mcp/helpers.go` (7 行): MCP 客户端、注册、重连和工具同步：helpers
- `internal/mcp/reconnect.go` (47 行): MCP 客户端、注册、重连和工具同步：reconnect
- `internal/mcp/registry.go` (323 行): MCP 客户端、注册、重连和工具同步：registry
- `internal/mcp/reloader.go` (26 行): MCP 客户端、注册、重连和工具同步：reloader
- `internal/mcp/tool_sync.go` (323 行): MCP 客户端、注册、重连和工具同步：tool sync
- `internal/mcp/types.go` (148 行): MCP 客户端、注册、重连和工具同步：types
- `internal/mcp/types_test.go` (41 行): 测试与回归验证：types

## `internal/memory/`

目录作用：记忆提取、存储、摘要、上下文和控制台。

- `internal/memory/category_test.go` (58 行): 测试与回归验证：category
- `internal/memory/console.go` (899 行): 记忆提取、存储、摘要、上下文和控制台：console
- `internal/memory/constants.go` (3 行): 记忆提取、存储、摘要、上下文和控制台：constants
- `internal/memory/context_builder.go` (614 行): 记忆提取、存储、摘要、上下文和控制台：context builder
- `internal/memory/embedding.go` (140 行): 记忆提取、存储、摘要、上下文和控制台：embedding
- `internal/memory/embedding_test.go` (14 行): 测试与回归验证：embedding
- `internal/memory/extractor.go` (87 行): 记忆提取、存储、摘要、上下文和控制台：extractor
- `internal/memory/feedback.go` (85 行): 记忆提取、存储、摘要、上下文和控制台：feedback
- `internal/memory/journal.go` (89 行): 记忆提取、存储、摘要、上下文和控制台：journal
- `internal/memory/lifecycle.go` (233 行): 记忆提取、存储、摘要、上下文和控制台：lifecycle
- `internal/memory/logging.go` (52 行): 记忆提取、存储、摘要、上下文和控制台：logging
- `internal/memory/safety.go` (113 行): 记忆提取、存储、摘要、上下文和控制台：safety
- `internal/memory/snapshot_renderer.go` (104 行): 记忆提取、存储、摘要、上下文和控制台：snapshot renderer
- `internal/memory/sqlite_store.go` (2129 行): 记忆提取、存储、摘要、上下文和控制台：sqlite store
- `internal/memory/store.go` (881 行): 记忆提取、存储、摘要、上下文和控制台：store
- `internal/memory/store_test.go` (1805 行): 测试与回归验证：store
- `internal/memory/summarizer.go` (535 行): 记忆提取、存储、摘要、上下文和控制台：summarizer
- `internal/memory/tool_records.go` (240 行): 记忆提取、存储、摘要、上下文和控制台：tool records
- `internal/memory/types.go` (393 行): 记忆提取、存储、摘要、上下文和控制台：types

## `internal/models/`

目录作用：模型/Provider 注册表和密钥处理。

- `internal/models/model_registry.go` (531 行): 模型/Provider 注册表和密钥处理：model registry
- `internal/models/model_registry_test.go` (179 行): 测试与回归验证：model registry
- `internal/models/provider_secret.go` (74 行): 模型/Provider 注册表和密钥处理：provider secret
- `internal/models/provider_secret_test.go` (107 行): 测试与回归验证：provider secret

## `internal/observability/`

目录作用：请求、工具和 Memory 日志能力。

- `internal/observability/logging.go` (34 行): 请求、工具和 Memory 日志能力：logging
- `internal/observability/memory_logger.go` (90 行): 请求、工具和 Memory 日志能力：memory logger
- `internal/observability/request_logger.go` (20 行): 请求、工具和 Memory 日志能力：request logger
- `internal/observability/tool_logger.go` (12 行): 请求、工具和 Memory 日志能力：tool logger

## `internal/reload/`

目录作用：运行态目录热重载。

- `internal/reload/catalog_reloader.go` (304 行): 运行态目录热重载：catalog reloader
- `internal/reload/catalog_reloader_test.go` (107 行): 测试与回归验证：catalog reloader

## `internal/resources/`

目录作用：内嵌资源入口。

- `internal/resources/embed.go` (6 行): 内嵌资源入口：embed

## `internal/resources/tools/`

目录作用：内置工具 YAML 定义。

- `internal/resources/tools/_memory_consolidate_.yml` (7 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/_memory_forget_.yml` (17 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/_memory_promote_.yml` (43 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/_memory_read_.yml` (21 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/_memory_search_.yml` (20 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/_memory_timeline_.yml` (17 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/_memory_update_.yml` (43 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/_memory_write_.yml` (25 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/_session_search_.yml` (18 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/_skill_candidate_list_.yml` (13 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/_skill_candidate_write_.yml` (29 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/agent_invoke.yml` (32 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/artifact_publish.yml` (30 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/ask_user_question.yml` (81 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/bash.yml` (24 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/bash_sandbox.yml` (29 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/datetime.yml` (14 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/plan_add_tasks.yml` (23 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/plan_get_tasks.yml` (9 行): 工具或服务 YAML 配置定义
- `internal/resources/tools/plan_update_task.yml` (26 行): 工具或服务 YAML 配置定义

## `internal/runctl/`

目录作用：运行控制轻量封装。

- `internal/runctl/runctl.go` (31 行): 运行控制轻量封装：runctl

## `internal/sandbox/`

目录作用：沙箱客户端、挂载和服务管理。

- `internal/sandbox/client.go` (247 行): 沙箱客户端、挂载和服务管理：client
- `internal/sandbox/client_test.go` (56 行): 测试与回归验证：client
- `internal/sandbox/mounts.go` (373 行): 沙箱客户端、挂载和服务管理：mounts
- `internal/sandbox/service.go` (337 行): 沙箱客户端、挂载和服务管理：service

## `internal/schedule/`

目录作用：调度任务注册、派发和编排。

- `internal/schedule/dispatcher.go` (120 行): 调度任务注册、派发和编排：dispatcher
- `internal/schedule/dispatcher_test.go` (134 行): 测试与回归验证：dispatcher
- `internal/schedule/orchestrator.go` (486 行): 调度任务注册、派发和编排：orchestrator
- `internal/schedule/orchestrator_test.go` (751 行): 测试与回归验证：orchestrator
- `internal/schedule/registry.go` (754 行): 调度任务注册、派发和编排：registry
- `internal/schedule/types.go` (88 行): 调度任务注册、派发和编排：types

## `internal/server/`

目录作用：HTTP/WebSocket 路由、Handler、查询和会话执行。

- `internal/server/admin_routes.go` (95 行): HTTP/WebSocket 路由、Handler、查询和会话执行：admin routes
- `internal/server/admin_routes_test.go` (126 行): 测试与回归验证：admin routes
- `internal/server/channel_test.go` (242 行): 测试与回归验证：channel
- `internal/server/channel_ws_test.go` (138 行): 测试与回归验证：channel ws
- `internal/server/chat_read_state.go` (92 行): HTTP/WebSocket 路由、Handler、查询和会话执行：chat read state
- `internal/server/deferred_awaiting.go` (60 行): HTTP/WebSocket 路由、Handler、查询和会话执行：deferred awaiting
- `internal/server/deferred_awaiting_test.go` (57 行): 测试与回归验证：deferred awaiting
- `internal/server/deferred_submit_test.go` (388 行): 测试与回归验证：deferred submit
- `internal/server/frame_orchestrator.go` (407 行): HTTP/WebSocket 路由、Handler、查询和会话执行：frame orchestrator
- `internal/server/frame_orchestrator_test.go` (406 行): 测试与回归验证：frame orchestrator
- `internal/server/handler_attach.go` (86 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler attach
- `internal/server/handler_catalog.go` (57 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler catalog
- `internal/server/handler_channel.go` (52 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler channel
- `internal/server/handler_chat.go` (234 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler chat
- `internal/server/handler_chat_delete.go` (57 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler chat delete
- `internal/server/handler_chat_export.go` (117 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler chat export
- `internal/server/handler_chat_search.go` (55 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler chat search
- `internal/server/handler_chat_search_test.go` (49 行): 测试与回归验证：handler chat search
- `internal/server/handler_feedback.go` (44 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler feedback
- `internal/server/handler_global_search.go` (45 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler global search
- `internal/server/handler_memory.go` (131 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler memory
- `internal/server/handler_memory_console.go` (295 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler memory console
- `internal/server/handler_memory_console_test.go` (293 行): 测试与回归验证：handler memory console
- `internal/server/handler_memory_test.go` (229 行): 测试与回归验证：handler memory
- `internal/server/handler_query.go` (748 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler query
- `internal/server/handler_query_integration_test.go` (1042 行): 测试与回归验证：handler query integration
- `internal/server/handler_query_test.go` (655 行): 测试与回归验证：handler query
- `internal/server/handler_resource.go` (167 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler resource
- `internal/server/handler_resource_integration_test.go` (353 行): 测试与回归验证：handler resource integration
- `internal/server/handler_run_stream_test.go` (67 行): 测试与回归验证：handler run stream
- `internal/server/handler_skill_candidates.go` (29 行): HTTP/WebSocket 路由、Handler、查询和会话执行：handler skill candidates
- `internal/server/memory_learning.go` (170 行): HTTP/WebSocket 路由、Handler、查询和会话执行：memory learning
- `internal/server/memory_learning_test.go` (134 行): 测试与回归验证：memory learning
- `internal/server/prompt_context.go` (553 行): HTTP/WebSocket 路由、Handler、查询和会话执行：prompt context
- `internal/server/prompt_context_test.go` (540 行): 测试与回归验证：prompt context
- `internal/server/proxy_handler.go` (308 行): HTTP/WebSocket 路由、Handler、查询和会话执行：proxy handler
- `internal/server/proxy_ws_handler.go` (613 行): HTTP/WebSocket 路由、Handler、查询和会话执行：proxy ws handler
- `internal/server/run_executor.go` (409 行): HTTP/WebSocket 路由、Handler、查询和会话执行：run executor
- `internal/server/run_executor_test.go` (246 行): 测试与回归验证：run executor
- `internal/server/security.go` (396 行): HTTP/WebSocket 路由、Handler、查询和会话执行：security
- `internal/server/server.go` (1004 行): HTTP/WebSocket 路由、Handler、查询和会话执行：server
- `internal/server/server_catalog_auth_test.go` (382 行): 测试与回归验证：server catalog auth
- `internal/server/server_hitl_test.go` (2058 行): 测试与回归验证：server hitl
- `internal/server/server_http_smoke_test.go` (120 行): 测试与回归验证：server http smoke
- `internal/server/server_test.go` (5052 行): 测试与回归验证：server
- `internal/server/server_ws_test.go` (1030 行): 测试与回归验证：server ws
- `internal/server/session_builder.go` (207 行): HTTP/WebSocket 路由、Handler、查询和会话执行：session builder
- `internal/server/session_builder_test.go` (32 行): 测试与回归验证：session builder
- `internal/server/shutdown_test.go` (319 行): 测试与回归验证：shutdown
- `internal/server/submit_resolution.go` (217 行): HTTP/WebSocket 路由、Handler、查询和会话执行：submit resolution
- `internal/server/submit_validation.go` (125 行): HTTP/WebSocket 路由、Handler、查询和会话执行：submit validation
- `internal/server/submit_validation_test.go` (74 行): 测试与回归验证：submit validation
- `internal/server/test_support_test.go` (807 行): 测试与回归验证：test support
- `internal/server/ws_regression_test.go` (398 行): 测试与回归验证：ws regression
- `internal/server/ws_routes.go` (891 行): HTTP/WebSocket 路由、Handler、查询和会话执行：ws routes

## `internal/skills/`

目录作用：Skill 候选和提取逻辑。

- `internal/skills/candidate_store.go` (207 行): Skill 候选和提取逻辑：candidate store
- `internal/skills/candidate_store_test.go` (43 行): 测试与回归验证：candidate store
- `internal/skills/extractor.go` (231 行): Skill 候选和提取逻辑：extractor
- `internal/skills/extractor_test.go` (37 行): 测试与回归验证：extractor

## `internal/stream/`

目录作用：流式事件、SSE、事件总线、组装和归一化。

- `internal/stream/artifact_test.go` (110 行): 测试与回归验证：artifact
- `internal/stream/assembler.go` (91 行): 流式事件、SSE、事件总线、组装和归一化：assembler
- `internal/stream/assembler_test.go` (118 行): 测试与回归验证：assembler
- `internal/stream/dispatcher.go` (1005 行): 流式事件、SSE、事件总线、组装和归一化：dispatcher
- `internal/stream/dispatcher_test.go` (839 行): 测试与回归验证：dispatcher
- `internal/stream/event.go` (402 行): 流式事件、SSE、事件总线、组装和归一化：event
- `internal/stream/event_bus.go` (349 行): 流式事件、SSE、事件总线、组装和归一化：event bus
- `internal/stream/event_bus_test.go` (236 行): 测试与回归验证：event bus
- `internal/stream/input.go` (264 行): 流式事件、SSE、事件总线、组装和归一化：input
- `internal/stream/normalizer.go` (90 行): 流式事件、SSE、事件总线、组装和归一化：normalizer
- `internal/stream/normalizer_test.go` (25 行): 测试与回归验证：normalizer
- `internal/stream/reasoning_label.go` (58 行): 流式事件、SSE、事件总线、组装和归一化：reasoning label
- `internal/stream/reasoning_label_test.go` (35 行): 测试与回归验证：reasoning label
- `internal/stream/source_test.go` (196 行): 测试与回归验证：source
- `internal/stream/sse.go` (283 行): 流式事件、SSE、事件总线、组装和归一化：sse
- `internal/stream/sse_test.go` (133 行): 测试与回归验证：sse
- `internal/stream/state.go` (67 行): 流式事件、SSE、事件总线、组装和归一化：state

## `internal/tools/`

目录作用：内置工具实现、路由、注册和参数处理。

- `internal/tools/bash_yaml_parity_test.go` (90 行): 测试与回归验证：bash yaml parity
- `internal/tools/builtin_tool_definitions.go` (86 行): 内置工具实现、路由、注册和参数处理：builtin tool definitions
- `internal/tools/builtin_tool_definitions_test.go` (182 行): 测试与回归验证：builtin tool definitions
- `internal/tools/datetime_memory_helpers.go` (363 行): 内置工具实现、路由、注册和参数处理：datetime memory helpers
- `internal/tools/helpers.go` (18 行): 内置工具实现、路由、注册和参数处理：helpers
- `internal/tools/lunar_date.go` (185 行): 内置工具实现、路由、注册和参数处理：lunar date
- `internal/tools/memory_logging.go` (28 行): 内置工具实现、路由、注册和参数处理：memory logging
- `internal/tools/tool_args.go` (87 行): 内置工具实现、路由、注册和参数处理：tool args
- `internal/tools/tool_artifact.go` (312 行): 内置工具实现、路由、注册和参数处理：tool artifact
- `internal/tools/tool_bash.go` (246 行): 内置工具实现、路由、注册和参数处理：tool bash
- `internal/tools/tool_bash_test.go` (401 行): 测试与回归验证：tool bash
- `internal/tools/tool_datetime.go` (15 行): 内置工具实现、路由、注册和参数处理：tool datetime
- `internal/tools/tool_definition_loader.go` (107 行): 内置工具实现、路由、注册和参数处理：tool definition loader
- `internal/tools/tool_executor.go` (173 行): 内置工具实现、路由、注册和参数处理：tool executor
- `internal/tools/tool_memory.go` (418 行): 内置工具实现、路由、注册和参数处理：tool memory
- `internal/tools/tool_memory_consolidate.go` (38 行): 内置工具实现、路由、注册和参数处理：tool memory consolidate
- `internal/tools/tool_memory_test.go` (456 行): 测试与回归验证：tool memory
- `internal/tools/tool_plan.go` (130 行): 内置工具实现、路由、注册和参数处理：tool plan
- `internal/tools/tool_registry.go` (144 行): 内置工具实现、路由、注册和参数处理：tool registry
- `internal/tools/tool_router.go` (291 行): 内置工具实现、路由、注册和参数处理：tool router
- `internal/tools/tool_sandbox_bash.go` (21 行): 内置工具实现、路由、注册和参数处理：tool sandbox bash
- `internal/tools/tool_sandbox_bash_test.go` (112 行): 测试与回归验证：tool sandbox bash
- `internal/tools/tool_session_search.go` (52 行): 内置工具实现、路由、注册和参数处理：tool session search
- `internal/tools/tool_session_search_test.go` (43 行): 测试与回归验证：tool session search
- `internal/tools/tool_skill_candidate.go` (53 行): 内置工具实现、路由、注册和参数处理：tool skill candidate
- `internal/tools/tool_skill_candidate_test.go` (45 行): 测试与回归验证：tool skill candidate

## `internal/viewport/`

目录作用：Viewport 服务和注册同步。

- `internal/viewport/registry.go` (75 行): Viewport 服务和注册同步：registry
- `internal/viewport/server_registry.go` (63 行): Viewport 服务和注册同步：server registry
- `internal/viewport/service.go` (46 行): Viewport 服务和注册同步：service
- `internal/viewport/service_test.go` (135 行): 测试与回归验证：service
- `internal/viewport/sync.go` (140 行): Viewport 服务和注册同步：sync

## `internal/ws/`

目录作用：WebSocket 连接、Hub、协议和 Handler。

- `internal/ws/conn.go` (499 行): WebSocket 连接、Hub、协议和 Handler：conn
- `internal/ws/conn_test.go` (189 行): 测试与回归验证：conn
- `internal/ws/handler.go` (137 行): WebSocket 连接、Hub、协议和 Handler：handler
- `internal/ws/handler_test.go` (52 行): 测试与回归验证：handler
- `internal/ws/hub.go` (62 行): WebSocket 连接、Hub、协议和 Handler：hub
- `internal/ws/hub_test.go` (55 行): 测试与回归验证：hub
- `internal/ws/protocol.go` (93 行): WebSocket 连接、Hub、协议和 Handler：protocol
- `internal/ws/protocol_test.go` (57 行): 测试与回归验证：protocol

## `internal/ws/gatewayclient/`

目录作用：Gateway WebSocket 客户端。

- `internal/ws/gatewayclient/client.go` (226 行): Gateway WebSocket 客户端：client
- `internal/ws/gatewayclient/client_test.go` (318 行): 测试与回归验证：client

## `scripts/`

目录作用：发布、校验、评测和辅助脚本。

- `scripts/gen-gateway-token.go` (149 行): 发布、校验、评测和辅助脚本：gen-gateway-token
- `scripts/gen-gateway-token_test.go` (225 行): 测试与回归验证：gen-gateway-token
- `scripts/memory-eval.sh` (75 行): Shell 脚本：memory eval
- `scripts/release-common.sh` (298 行): Shell 脚本：release common
- `scripts/release-program.ps1` (338 行): PowerShell 脚本：release program
- `scripts/release-program.sh` (140 行): Shell 脚本：release program
- `scripts/release.sh` (5 行): Shell 脚本：release
- `scripts/verify-memory.sh` (118 行): Shell 脚本：verify memory

## `scripts/release-assets/program/`

目录作用：发布包模板与说明。

- `scripts/release-assets/program/manifest.template.json` (111 行): JSON 模板/清单文件

## `scripts/release-assets/program/unix/`

目录作用：Unix 发布包部署与启停脚本。

- `scripts/release-assets/program/unix/deploy.sh` (16 行): Shell 脚本：deploy
- `scripts/release-assets/program/unix/program-common.sh` (259 行): Shell 脚本：program common
- `scripts/release-assets/program/unix/start.sh` (32 行): Shell 脚本：start
- `scripts/release-assets/program/unix/stop.sh` (11 行): Shell 脚本：stop

## `scripts/release-assets/program/windows/`

目录作用：Windows 发布包部署与启停脚本。

- `scripts/release-assets/program/windows/deploy.ps1` (11 行): PowerShell 脚本：deploy
- `scripts/release-assets/program/windows/program-common.ps1` (161 行): PowerShell 脚本：program common
- `scripts/release-assets/program/windows/start.ps1` (18 行): PowerShell 脚本：start
- `scripts/release-assets/program/windows/stop.ps1` (7 行): PowerShell 脚本：stop
