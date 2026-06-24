# CLAUDE.md

## 1. 项目概览

`agent-platform` 是 `agent-platform` 的 Go 版运行时仓库，目标是在保持 Java runtime 接口风格与部署方式尽量一致的前提下，逐步形成可独立运行的 agent runtime。

当前仓库定位是“最小可运行闭环 + 特色能力持续补齐”：

- 已具备独立 HTTP 服务、统一 JSON 包裹与 `POST /api/query` 真流式 SSE。
- 已具备 chat 摘要、事件流、raw messages、上传资源落盘、归档与搜索。
- 已具备目录驱动的 agents / teams / skills / tools catalog。
- 已具备 OpenAI / Anthropic 协议模型调用、backend tools、Container Hub sandbox 与 tools。
- 已具备 HITL question / approval / form、运行中 submit / steer / interrupt 协议入口。
- 已具备 SQLite memory、FTS、可选 embedding、learn / consolidate / feedback 与 memory tools。
- 已具备 automation、`agent_invoke` 子智能体调度、MCP tool sync、WebSocket 控制面等能力骨架。
尚未完全对齐 Java 版的部分能力包括 frontend tool 完整闭环、MCP 全量生产验证、automation 深度编排、热重载细节和更完整的前端协议适配。未落地能力必须在专题文档中明确标注，不能写成已完成能力。

## 2. 技术栈

- 语言：Go
- HTTP：标准库 `net/http`
- 序列化：标准库 `encoding/json`
- 存储：本地文件系统 + SQLite memory store
- 配置：环境变量 + `configs/*.yml`

当前没有引入 Web 框架、第三方路由库、外部数据库或消息队列。配置默认值以 `internal/config/config.go` 与 `configs/*.example.yml` 为事实源。

## 3. 架构设计

启动装配主链路：

```text
cmd/agent-platform/main.go
  -> app.New()
  -> config.Load()
  -> chat store / memory store / catalog registry
  -> model registry / MCP registry / gateway registry
  -> sandbox service / runtime tool executor
  -> LLM agent engine / automation orchestrator
  -> server.New()
```

核心模块边界：

- `internal/server`：HTTP 路由、请求校验、响应包裹、SSE / WebSocket 协调。
- `internal/llm`：模型协议、prompt 构建、run stream、HITL、planning、tool loop。
- `internal/tools`：backend tool registry、Bash、FileTools、memory、desktop、MCP tool 调用。
- `internal/chat`：chat 摘要、事件、StepLine、raw messages、资源文件、归档、回放。
- `internal/memory`：SQLite memory、FTS、embedding、生命周期整理、反馈循环。
- `internal/catalog`：agent / team / skill / tool 目录装载与定义解析。
- `internal/config`：环境变量、YAML、默认值。
- `internal/stream`：统一事件、dispatcher、SSE writer、事件归一化。
- `internal/sandbox`：Container Hub client、mounts、sandbox 执行。
- `internal/automation`：automation 注册、调度、执行记录。
- `internal/ws` 与 `internal/gateway`：WebSocket 控制面与反向 gateway 连接。

## 4. 目录结构

```text
.
├── cmd/agent-platform/          # 进程入口
├── configs/                     # 配置模板与本地覆写入口
├── docs/                        # 中文专题文档
├── internal/                    # Go runtime 实现
├── scripts/                     # 审计和辅助脚本
├── Dockerfile
├── Makefile
├── compose.yml
├── README.md
└── VERSION
```

`docs/` 是特色能力的主说明区；`CLAUDE.md` 只保留项目事实总览、开发入口和专题索引。

## 5. 数据结构

Chat 默认由 `CHATS_DIR` 控制，主要包含：

- `chats.db`：chat 摘要索引。
- `<chatId>.jsonl`：运行事件、StepLine、system init 与 raw messages。
- `<chatId>/<uploaded-file>`：上传资源文件。

Memory 默认由 `MEMORY_DIR` 控制，当前以 SQLite store 为主，支持 FTS、可选 embedding、observation / fact 生命周期、`/api/learn` 与 memory tools。

核心 DTO 位于 `internal/api`，包括 query、submit、steer、interrupt、learn、chat、upload、automation、memory console 等请求和响应类型。

## 6. API 定义

所有非 SSE JSON 接口统一返回：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

主要接口分组：

- Catalog：`/api/agents`、`/api/agent`、`/api/teams`、`/api/admin/skills`、`/api/admin/tools`。
- Chat：`/api/chats`、`/api/chat`、`/api/chats/search`、`/api/read`、`/api/chat/export`。
- Archive：`/api/archives`、`/api/archive`、`/api/archives/search`。
- Run：`/api/query`、`/api/attach`、`/api/submit`、`/api/steer`、`/api/interrupt`。
- Memory：`/api/learn`、memory console 相关接口。
- Resource：`/api/upload`、`/api/resource`。
- Viewport / WebSocket：`/api/viewport`、`/ws`。

详细协议拆分到专题文档：REST / SSE / WebSocket 见 [API与协议](docs/API与协议.md)，真流式与 attach 见 [真流式和H2A](docs/真流式和H2A.md)，HITL 见 [HITL协议](docs/HITL协议.md)。

## 7. 开发要点

- 配置事实源以 `internal/config/config.go` 和 `configs/*.example.yml` 为准，文档只解释和引用。
- `.env`、真实 `configs/*.yml`、真实 `configs/*.pem`、真实 token 和私钥不得提交。
- 工具运行时配置以 `configs/tools.yml` 为外部事实源，包含 access policy、bash 和 file tools。
- `configs/tools.yml` 中的旧 YAML 键（如 `bash.allowed-paths`、`file-tools.allowed-read-paths`）会在启动阶段硬失败，新策略统一走 `tools.access-policy`。
- 新增能力优先放进对应 `internal/*` 模块，不在 server 层堆业务逻辑。
- 新增 API 保持统一 JSON 包裹、字段命名和错误语义。
- 测试以 `make test` / `go test ./...` 为主，协议变更优先覆盖 `internal/server`、`internal/stream`、`internal/llm`、`internal/tools`。

## 8. 开发流程

本地开发：

```bash
cp .env.example .env
make run
make test
```

涉及文档、配置或目录规范调整时，同步检查 `README.md`、`CLAUDE.md`、`docs/` 与 `.gitignore`。

## 9. 已知约束与注意事项

- `configs/` 下配置启动时读取，运行中修改需要重启 runtime。
- `POST /api/query` 默认逐事件 flush；启用 `configs/runtime.yml -> h2a.render.*` 缓冲后，客户端看到的输出可能不再逐事件抵达。
- WebSocket 是控制面，浏览器/普通客户端文件字节仍走 `POST /api/upload` 和 `GET /api/resource`。
- `runtimeConfig.env` 不会通过 catalog API 回显，避免泄露代理、凭据或私有 endpoint。
- 文件工具权限独立于 Bash 权限，越权路径通过 HITL approval 兜底。
- `agent_invoke` 只允许显式配置的主 agent 使用，当前禁止嵌套。

## 特色功能文档索引

- [智能体配置说明](docs/智能体配置说明.md)：agent / team / skill 定义、CODER、prompt files、memoryConfig、runtimeConfig。
- [配置化说明](docs/配置化说明.md)：环境变量、`configs/*.yml`、默认值、优先级、废弃变量。
- [工具目录权限](docs/工具目录权限.md)：Bash、FileTools、allowed paths、越权审批、读后写闭环。
- [真流式和H2A](docs/真流式和H2A.md)：SSE、heartbeat、`[DONE]`、attach、backlog、H2A 缓冲。
- [记忆系统](docs/记忆系统.md)：remember、SQLite memory、FTS、embedding、learn、consolidate、memory tools。
- [运行时和沙箱](docs/运行时和沙箱.md)：runtime 目录、Container Hub、mounts、host / sandbox 工具边界。
- [API与协议](docs/API与协议.md)：HTTP API 参数、SSE、WebSocket、HTTP 文件数据面、resource ticket。
- [HITL协议](docs/HITL协议.md)：question / approval / form、submit、awaiting 事件。
- [自动化](docs/自动化.md)：automation registry、orchestrator、dispatch、执行记录。
- [子智能体调度](docs/子智能体调度.md)：`agent_invoke`、task events、子流归并、禁止嵌套。
- [MCP与前端工具](docs/MCP与前端工具.md)：MCP registry、tool sync、frontend tool 当前边界。
- [会话存储与回放](docs/会话存储与回放.md)：chat store、StepLine、raw messages、archive、search、resource。
- [鉴权与安全边界](docs/鉴权与安全边界.md)：JWT、JWKS、本地公钥、resource ticket、CORS、敏感配置。
- [版本化打包方案](docs/版本化打包方案.md)：README 索引的交付专题文档。
- [手工测试用例](docs/手工测试用例.md)：curl 回归用例。
