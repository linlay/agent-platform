# AGENTS.md

## 1. 项目概览

`agent-platform` 是 `agent-platform` 的 Go 版运行时仓库，目标是在保持 Java runtime 接口风格与部署方式尽量一致的前提下，逐步形成可独立运行的 agent runtime。

当前仓库定位是“最小可运行闭环 + 特色能力持续补齐”：

- 已具备独立 HTTP 服务、统一 JSON 包裹与 `POST /api/query` 真流式 SSE。
- 已具备 chat 摘要、事件流、raw messages、上传资源落盘、归档与搜索。
- 已具备目录驱动的 agents / teams / skills / tools catalog。
- 已具备 OpenAI / Anthropic 协议模型调用、backend tools、Container Hub sandbox 与 tools。
- 已具备由 platform lock 固定并随服务包分发的 Host builtins（rg/dbx/httpx/kbase-lance-engine/poppler-pdftotext）；`file_grep/file_glob` 稳定包装 rg，dbx/httpx 保持 CLI，KBASE PDF 默认调用 Poppler `pdftotext` launcher。
- 已具备 HITL question / approval / form、运行中 submit / steer / interrupt 协议入口。
- 已具备 SQLite memory、FTS、可选 embedding、learn / consolidate / feedback 与 memory tools。
- 已具备 KBASE 文本知识库的 LanceDB generation 检索、加权 RRF 与本地 Rust sidecar 管理；SQLite `control.db` 只负责 generation、文件状态与恢复日志。
- 已具备 automation、`agent_invoke` 子智能体调度、带隐藏协调器的 orchestrated Team、MCP tool sync、WebSocket 控制面等能力骨架。
尚未完全对齐 Java 版的部分能力包括 frontend tool 完整闭环、MCP 全量生产验证、automation 深度编排、热重载细节和更完整的前端协议适配。未落地能力必须在专题文档中明确标注，不能写成已完成能力。

## 2. 技术栈

- 语言：Go
- HTTP：标准库 `net/http`
- 序列化：标准库 `encoding/json`
- 存储：本地文件系统 + SQLite memory/control store + 本地 LanceDB KBASE generation
- 配置：环境变量 + `configs/*.yml`

当前没有引入 Web 框架、第三方路由库、外部数据库或消息队列。Go 主程序仍以 `CGO_ENABLED=0` 构建；KBASE 通过随包分发的 `kbase-lance-engine` Rust 伴随进程使用锁定的 LanceDB Rust SDK。配置默认值以 `internal/config/config.go` 与 `configs/*.example.yml` 为事实源。

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

- `internal/agent`：中立 mode 契约、公共 prompt 模板变量与 system-init spec；`internal/agent/builtin` 是 CODER/KBASE/TEAM 的静态分派点。
- `internal/agent/coder`：CODER profile、prompt、planning、ACP/workspace 策略与创建默认策略。
- `internal/agent/kbase`：KBASE profile、配置/默认值、prompt、索引、watcher、Lance retrieval、control store、generation/恢复、sidecar supervisor、HTTP 业务错误与五个工具 handler。
- `internal/agent/team`：内部 TEAM profile、硬编码调度规则、成员 roster prompt、session-local 隐藏工具与调度状态机；TEAM 不能配置成普通 agent。
- `internal/server`：HTTP 路由、请求校验、响应包裹、SSE / WebSocket 协调。
- `internal/llm`：模型协议、prompt 构建、run stream、HITL、planning、tool loop。
- `internal/tools`：通用 tool registry/router、Bash、FileTools、memory、desktop、MCP tool 调用；mode 工具通过命名 handler 接入，不在 executor 中增加 mode switch。
- `internal/chat`：chat 摘要、事件、StepLine、raw messages、资源文件、归档、回放。
- `internal/memory`：SQLite memory、FTS、embedding、生命周期整理、反馈循环。
- `internal/catalog`：agent / team / skill / tool 目录装载与定义解析；区分 legacy Team 与目录式 orchestrated Team，并以原子快照冻结成员、协调器配置和 prompt。
- `internal/config`：环境变量、YAML、默认值。
- `internal/stream`：统一事件、dispatcher、SSE writer、事件归一化。
- `internal/sandbox`：Container Hub client、mounts、sandbox 执行。
- `internal/automation`：automation 注册、调度、执行记录。
- `internal/ws` 与 `internal/gateway`：WebSocket 控制面与反向 gateway 连接。

这里没有类继承：`internal/agent` 是中立契约层，`internal/agent/coder`、`internal/agent/kbase` 与 `internal/agent/team` 是该契约下的三个内置实现，`internal/agent/builtin` 只负责静态分派。TEAM 是仅由 orchestrated Team 在 run 内合成的内部 mode；隐藏协调器不进入普通 agent catalog，也不能通过普通 Agent YAML 或管理接口创建。

## 4. 目录结构

```text
.
├── cmd/agent-platform/          # 进程入口
├── configs/                     # 配置模板与本地覆写入口
├── docs/                        # 中文专题文档
├── internal/                    # Go runtime 实现
│   └── agent/                   # 中立 mode 契约及 CODER/KBASE/TEAM 特有实现
├── build/                       # 忽略的多平台 builtin 本地装配缓存
├── scripts/                     # 审计和辅助脚本
├── Dockerfile
├── Makefile
├── compose.yml
├── README.md
└── VERSION
```

`docs/` 是特色能力的主说明区；当前项目事实文件 `AGENTS.md` 只保留事实总览、开发入口和专题索引。

## 5. 数据结构

Chat 默认由 `AP_RUNTIME_CHATS_DIR` 控制，主要包含：

- `chats.db`：chat 摘要索引。
- `<chatId>.jsonl`：运行事件、StepLine、system init 与 raw messages。
- `<chatId>/<uploaded-file>`：上传资源文件。

Memory 默认由 `AP_RUNTIME_MEMORY_DIR` 控制，当前以 SQLite store 为主，支持 FTS、可选 embedding、observation / fact 生命周期、`/api/learn` 与 memory tools。

KBASE 默认由 `AP_RUNTIME_KBASE_DIR` 控制，每个 agent storageDir 可包含：

- `control.db`：schema v3 控制面，记录 generation、文件状态、file operation 和 index run；不保存 chunk、FTS 或 embedding。
- `generations/<generationId>/lance/`：LanceDB chunks table 及索引；同级 `manifest.json` 保存 generation 元数据。

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
- KBASE：`/api/kbase/{agentKey}/status`、`/api/kbase/{agentKey}/refresh` 以及五个 KBASE tools。
- Resource：`/api/upload`、`/api/resource`。
- Viewport / WebSocket：`/api/viewport`、`/ws`。

详细协议拆分到专题文档：REST / SSE / WebSocket 见 [API与协议](docs/API与协议.md)，真流式与 attach 见 [真流式和H2A](docs/真流式和H2A.md)，HITL 见 [HITL协议](docs/HITL协议.md)。

## 7. 开发要点

- 通用运行时配置事实源以 `internal/config/config.go` 和 `configs/*.example.yml` 为准；CODER/KBASE/TEAM 的 profile、默认值和业务规则分别以 `internal/agent/coder`、`internal/agent/kbase`、`internal/agent/team` 为准，文档只解释和引用。
- `.env`、真实 `configs/*.yml`、真实 `configs/*.pem`、真实 token 和私钥不得提交。
- 工具运行时配置以 `configs/tools.yml` 为外部事实源，包含 access policy、bash 和 file tools。
- `configs/tools.yml` 中的旧 YAML 路径策略键（如 `bash.allowed-paths`、`file-tools.allowed-read-paths`）会在启动阶段硬失败；Go 配置结构中的旧路径字段也已删除，目录权限统一走 `tools.access-policy`。
- 新增能力优先放进对应 `internal/*` 模块，不在 server 层堆业务逻辑。
- TEAM 是内部专用 mode：公共机制进入 `internal/agent`，调度规则进入 `internal/agent/team`。普通 `AgentDefinition` 必须拒绝 `mode: TEAM`，隐藏协调器不得注册到 `/api/agents`、`/api/agent` 或普通 `agent_invoke` 目标中。
- 新增 API 保持统一 JSON 包裹、字段命名和错误语义。
- KBASE 对外 tool/REST/`source.publish` 契约以 LanceDB 路径回归；只有 `indexHash` 变化可触发新 generation，`queryHash` 中的 topK/RRF/权重/候选池调整不得引发全量重建。
- 测试以 `make test` / `go test ./...` 为主，协议变更优先覆盖 `internal/server`、`internal/stream`、`internal/llm`、`internal/tools`。

## 8. 开发流程

本地开发：

```bash
cp .env.example .env
./scripts/sync-local-builtins.sh
make run
make test
```

首次本地运行或更新相邻 builtin 项目后，先执行 `./scripts/sync-local-builtins.sh`；它在隔离工作目录中构建本机 `dbx`、`httpx` 与 Rust sidecar，并原子更新 `build/builtins/<host>/`。`rg` 仍使用相邻 collection 中的校验后 vendor artifact。`--all` 仅用于具备完整 Rust 交叉编译环境的 release runner。同步脚本不写 `release-local/`；`make run` / `make build-local` 不得重新引入 builtin 或 Rust 构建步骤，运行时只从本机 build cache 使用 builtin。

涉及文档、配置或目录规范调整时，同步检查 `README.md`、`AGENTS.md`、`docs/` 与 `.gitignore`。

## 9. 已知约束与注意事项

- `configs/` 下配置启动时读取，运行中修改需要重启 runtime。
- `POST /api/query` 默认逐事件 flush；启用 `configs/runtime.yml -> h2a.render.*` 缓冲后，客户端看到的输出可能不再逐事件抵达。
- WebSocket 是控制面，浏览器/普通客户端文件字节仍走 `POST /api/upload` 和 `GET /api/resource`。
- `runtimeConfig.env` 不会通过 catalog API 回显，避免泄露代理、凭据或私有 endpoint。
- 文件工具权限独立于 Bash 权限，越权路径通过 HITL approval 兜底。
- `agent_invoke` 只允许显式配置的普通主 agent 使用，当前禁止嵌套；orchestrated Team 自动注入 session-local embedded builtin `agent_delegate` 和三个 plan tools。普通 Agent 配置、session 与执行入口均拒绝 `agent_delegate`，该工具也不进入公开工具 catalog。
- chat 创建后 `teamId` 固定。legacy Team 以所选成员为 run owner；orchestrated Team 以 `teamId` 为公开 owner，隐藏协调器 key 只用于进程内执行，不得作为公共 Agent 身份回显。
- Team 成员、成员定义、协调器配置与 prompt 在 run 开始时解析为快照，运行中 catalog 热重载不改变该 run；下一次 run 才读取新快照。
- KBASE Lance sidecar 只监听 loopback，由 Go 生成一次性 Bearer token 并监督生命周期。存在 KBASE agent 时 sidecar 必须可用；无 active generation 时 search 返回 stale 并触发冷建，sidecar 故障显式返回 unavailable，绝不回退旧 SQLite 文件。这些故障不影响非 KBASE 模块启动。
- 当前 KBASE 只对文本抽取结果做 embedding/FTS；PDF/DOCX/PPTX/HTML 均是先抽取文本，不得宣称支持图片、音频或视频语义检索。

## 特色功能文档索引

- [智能体配置说明](docs/智能体配置说明.md)：agent / team / skill 定义、CODER、KBASE、legacy/orchestrated Team、prompt files、memoryConfig、runtimeConfig。
- [配置化说明](docs/配置化说明.md)：环境变量、`configs/*.yml`、默认值、优先级、废弃变量。
- [工具目录权限](docs/工具目录权限.md)：Bash、FileTools、allowed paths、越权审批、读后写闭环。
- [真流式和H2A](docs/真流式和H2A.md)：SSE、heartbeat、`[DONE]`、attach、backlog、H2A 缓冲。
- [记忆系统](docs/记忆系统.md)：remember、SQLite memory、FTS、embedding、learn、consolidate、memory tools。
- [运行时和沙箱](docs/运行时和沙箱.md)：runtime 目录、Container Hub、mounts、host / sandbox 工具边界。
- [KBASE LanceDB 迁移](docs/KBASE-LanceDB迁移.md)：LanceDB sidecar、control.db、generation、加权 RRF、迁移验证、恢复、回滚与分发边界。
- [API与协议](docs/API与协议.md)：HTTP API 参数、SSE、WebSocket、HTTP 文件数据面、resource ticket。
- [HITL协议](docs/HITL协议.md)：question / approval / form、submit、awaiting 事件。
- [自动化](docs/自动化.md)：automation registry、orchestrator、dispatch、执行记录。
- [子智能体调度](docs/子智能体调度.md)：`agent_invoke` 与 TEAM 隐藏调度、task events、子流归并、禁止嵌套。
- [MCP与前端工具](docs/MCP与前端工具.md)：MCP registry、tool sync、frontend tool 当前边界。
- [会话存储与回放](docs/会话存储与回放.md)：chat store、StepLine、raw messages、archive、search、resource。
- [鉴权与安全边界](docs/鉴权与安全边界.md)：JWT、JWKS、本地公钥、resource ticket、CORS、敏感配置。
- [版本化打包方案](docs/版本化打包方案.md)：README 索引的交付专题文档。
- [手工测试用例](docs/手工测试用例.md)：curl 回归用例。
