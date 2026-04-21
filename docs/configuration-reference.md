# Configuration 参考

当前 Go runner 的配置事实源主要来自两部分：

- 代码默认值：`internal/config/config.go`
- 外部输入：环境变量与 `configs/*.yml`

`.env.example` 现在只保留推荐给普通部署者的最终用户配置入口。本页则保留完整的高级、排障、兼容和低频环境变量说明；变量仍然受运行时支持，并不代表它们都应该暴露给最终用户。

## 标签说明

| 标签 | 含义 |
|---|---|
| `End user` | 普通部署者通常会直接理解、填写或调整的变量 |
| `Advanced / operator` | 运维、高级部署或容量调优场景才需要改动 |
| `Debug / troubleshooting` | 主要用于排障、链路观测和临时诊断 |
| `Internal / compatibility` | 偏实现细节或兼容用途，不建议作为公开产品入口 |
| `Wired but not recommended for public use` | 代码会读取，但当前 Go runner 并未形成稳定、清晰的终端用户能力，不建议公开暴露 |

## 推荐放进 `.env` 的最终用户变量

### Server

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `HOST_PORT` | `11949` | `End user` | 本地 `make run` 与 `compose.yml` 的主入口端口 |
| `SERVER_PORT` | `8080` | `End user` | 应用内部监听端口；`make run` 会优先用 `HOST_PORT` 覆盖到本地监听端口 |

本地运行约定：

- `make run` 会先加载根目录 `.env`
- `make run` 的端口链路为 `HOST_PORT -> SERVER_PORT -> 8080`
- 直接执行 `go run ./cmd/agent-platform-runner` 不会自动加载 `.env`
- 直接 `go run` 时，应用端口链路仍然是 `SERVER_PORT -> 8080`

### Auth / Ticket

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `AGENT_AUTH_ENABLED` | `true` | `End user` | 是否开启 `/api/**` JWT 鉴权 |
| `AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE` | `configs/local-public-key.pem` | `Advanced / operator` | 本地公钥模式下的公钥文件路径 |
| `AGENT_AUTH_JWKS_URI` | 空 | `End user` | JWKS 模式的公钥地址 |
| `AGENT_AUTH_ISSUER` | 空 | `End user` | JWT issuer 校验值 |
| `AGENT_AUTH_JWKS_CACHE_SECONDS` | `0` | `Advanced / operator` | JWKS 缓存秒数，仅在 JWKS 模式下生效 |
| `CHAT_IMAGE_TOKEN_SECRET` | 空 | `End user` | chat image token 的签名密钥 |
| `CHAT_IMAGE_TOKEN_TTL_SECONDS` | `86400` | `Advanced / operator` | token 过期时间 |
| `CHAT_RESOURCE_TICKET_ENABLED` | `true` | `End user` | `/api/resource` 是否允许 resource ticket 访问 |

本地 JWT 公钥规则：

- 默认值是 `configs/local-public-key.pem`
- `AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE` 若是绝对路径，则原样使用
- 若是相对路径，则按项目根目录解析
- 若为了兼容仍写成单文件名 `local-public-key.pem`，会自动解析到 `configs/local-public-key.pem`

### 目录配置

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `REGISTRIES_DIR` | `runtime/registries` | `End user` | provider / model registry 根目录 |
| `OWNER_DIR` | `runtime/owner` | `End user` | owner 目录 |
| `AGENTS_DIR` | `runtime/agents` | `End user` | agents 定义目录 |
| `TEAMS_DIR` | `runtime/teams` | `End user` | teams 定义目录 |
| `ROOT_DIR` | `runtime/root` | `End user` | runner 根目录映射 |
| `SCHEDULES_DIR` | `runtime/schedules` | `End user` | schedules 定义目录 |
| `CHATS_DIR` | `runtime/chats` | `End user` | chat 落盘目录 |
| `MEMORY_DIR` | `runtime/memory` | `End user` | memory 落盘目录 |
| `PAN_DIR` | `runtime/pan` | `End user` | pan 目录映射 |
| `SKILLS_MARKET_DIR` | `runtime/skills-market` | `End user` | skills market 目录映射 |
| `PROVIDER_APIKEY_KEY_PART` | 空 | `Advanced / operator` | provider `apiKey: AES(...)` 的环境变量半密钥 |

### Container Hub

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `AGENT_CONTAINER_HUB_ENABLED` | `false` | `End user` | 是否启用 Container Hub |
| `AGENT_CONTAINER_HUB_BASE_URL` | `http://127.0.0.1:11960` | `End user` | Container Hub 服务地址 |
| `AGENT_CONTAINER_HUB_AUTH_TOKEN` | 空 | `End user` | Bearer token |
| `AGENT_CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID` | 空 | `End user` | 默认 environment id |
| `AGENT_CONTAINER_HUB_REQUEST_TIMEOUT_MS` | `300000` | `Advanced / operator` | 请求超时 |
| `AGENT_CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL` | `run` | `Advanced / operator` | 默认沙箱级别 |
| `AGENT_CONTAINER_HUB_AGENT_IDLE_TIMEOUT_MS` | `600000` | `Advanced / operator` | agent 级沙箱闲置回收时间 |
| `AGENT_CONTAINER_HUB_DESTROY_QUEUE_DELAY_MS` | `5000` | `Advanced / operator` | 销毁队列延迟 |

Container Hub 默认基础挂载为：

- `/workspace` -> `CHATS_DIR/<chatId>`（`rw`）
- `/root` -> `ROOT_DIR`（`rw`）
- `/skills` -> `AGENTS_DIR/<agentKey>/skills`（`run/agent`）或 `SKILLS_MARKET_DIR`（`global`），`ro`
- `/pan` -> `PAN_DIR`（`rw`）
- `/agent` -> `AGENTS_DIR/<agentKey>`（`ro`，必挂载；缺失时 fail-fast）
- `/owner` -> `OWNER_DIR`（`ro`，缺失时自动创建）
- `/memory` -> `MEMORY_DIR/<agentKey>`（`ro`，缺失时自动创建）

说明：

- `/memory` 当前只影响沙箱挂载与 prompt 中的路径暴露；runner 自身 memory store 仍保持现有 `MEMORY_DIR` 存储布局
- `sandboxConfig.extraMounts` 会真实影响 Container Hub session payload，并在基础挂载生成后应用覆盖规则
- `platform: agent` / `platform: owner` / `platform: memory` 主要用于覆盖默认 `/agent` / `/owner` / `/memory` 的只读模式，不会新增第二个挂载
- `destination + mode` 是覆盖默认基础挂载模式的唯一合法写法
- `source + destination + mode` 只能新增非默认目标路径挂载；若目标是默认基础挂载路径会直接报错

### Global Agent Defaults

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `AGENT_DEFAULT_MAX_TOKENS` | `4096` | `Advanced / operator` | 全局默认 `maxTokens` |
| `AGENT_DEFAULT_BUDGET_RUN_TIMEOUT_MS` | `300000` | `Advanced / operator` | run 默认预算超时 |
| `AGENT_DEFAULT_BUDGET_MODEL_MAX_CALLS` | `30` | `Advanced / operator` | 模型最大调用次数 |
| `AGENT_DEFAULT_BUDGET_MODEL_TIMEOUT_MS` | `120000` | `Advanced / operator` | 模型调用超时 |
| `AGENT_DEFAULT_BUDGET_MODEL_RETRY_COUNT` | `0` | `Advanced / operator` | 模型调用重试次数 |
| `AGENT_DEFAULT_BUDGET_TOOL_MAX_CALLS` | `20` | `Advanced / operator` | 工具最大调用次数 |
| `AGENT_DEFAULT_BUDGET_TOOL_TIMEOUT_MS` | `120000` | `Advanced / operator` | 工具调用超时 |
| `AGENT_DEFAULT_BUDGET_TOOL_RETRY_COUNT` | `0` | `Advanced / operator` | 工具调用重试次数 |
| `AGENT_DEFAULT_REACT_MAX_STEPS` | `60` | `Advanced / operator` | React 模式最大步数 |
| `AGENT_DEFAULT_PLAN_EXECUTE_MAX_STEPS` | `60` | `Advanced / operator` | Plan Execute 默认最大步数 |
| `AGENT_DEFAULT_PLAN_EXECUTE_MAX_WORK_ROUNDS_PER_TASK` | `6` | `Advanced / operator` | 单任务默认最大 work rounds |

## 高级运维变量

### Schedule

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `AGENT_SCHEDULE_ENABLED` | `true` | `Advanced / operator` | 是否启用 schedule orchestrator |
| `AGENT_SCHEDULE_DEFAULT_ZONE_ID` | 空 | `Advanced / operator` | 默认 schedule 时区；未配置时回退到宿主机本地时区 |
| `AGENT_SCHEDULE_POOL_SIZE` | `4` | `Advanced / operator` | schedule dispatch 最大并发数 |

普通部署通常不需要显式配置 `AGENT_SCHEDULE_*`；只有在需要固定业务时区，或需要限制/放大 schedule 并发时才建议调整。

### Bash 工具

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `AGENT_BASH_WORKING_DIRECTORY` | `.` | `Advanced / operator` | `_bash_` 默认工作目录 |
| `AGENT_BASH_ALLOWED_PATHS` | `.,/tmp` | `Advanced / operator` | 允许访问的路径白名单 |
| `AGENT_BASH_ALLOWED_COMMANDS` | `ls,pwd,cat,head,tail,top,free,df,git,rg,find` | `Advanced / operator` | 允许执行的命令白名单 |
| `AGENT_BASH_PATH_CHECKED_COMMANDS` | `ls,cat,head,tail,git,rg,find` | `Advanced / operator` | 开启路径校验的命令 |
| `AGENT_BASH_PATH_CHECK_BYPASS_COMMANDS` | 空 | `Internal / compatibility` | 跳过路径校验的命令 |
| `AGENT_BASH_SHELL_FEATURES_ENABLED` | `false` | `Advanced / operator` | shell 高级语法开关 |
| `AGENT_BASH_SHELL_EXECUTABLE` | `bash` | `Advanced / operator` | shell 可执行文件 |
| `AGENT_BASH_SHELL_TIMEOUT_MS` | `10000` | `Advanced / operator` | shell 模式超时 |
| `AGENT_BASH_MAX_COMMAND_CHARS` | `16000` | `Advanced / operator` | 最大命令长度 |
| `AGENT_BASH_HITL_DEFAULT_TIMEOUT_MS` | `120000` | `Advanced / operator` | bash HITL 默认超时 |

`bash HITL` 不再有单独的 `.env` 开关。是否拦截 bash 命令完全由当前 agent 挂载的 skills 决定：规则来自 skill 目录下的 `.bash-hooks/`，未挂载任何 bash hooks 时不会触发拦截。`AGENT_BASH_HITL_DEFAULT_TIMEOUT_MS` 仍用于控制审批等待超时，但不再对应任何全局 `registries/bash-hitl` 目录。

### Memory 行为调优

以下变量会真实影响当前 Go runner 的 memory 召回行为，但仍属于高级调优项，不建议默认暴露给最终用户：

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `AGENT_MEMORY_DB_FILE_NAME` | `memory.db` | `Advanced / operator` | memory SQLite 文件名 |
| `AGENT_MEMORY_CONTEXT_TOP_N` | `5` | `Advanced / operator` | query 前置 memory 召回条数 |
| `AGENT_MEMORY_CONTEXT_MAX_CHARS` | `4000` | `Advanced / operator` | 注入 prompt 的 memory context 最大字符数 |
| `AGENT_MEMORY_SEARCH_DEFAULT_LIMIT` | `10` | `Advanced / operator` | `_memory_*` 工具默认 limit |

agent definition 侧另有 `memoryConfig`：

- 默认会给 agent 注入基础 memory tools：`_memory_write_`、`_memory_read_`、`_memory_search_`
- `memoryConfig.enabled: false` 可以显式关闭基础 memory tools 注入
- `memoryConfig.managementTools: true` 会额外注入管理类 tools：`_memory_update_`、`_memory_forget_`、`_memory_timeline_`、`_memory_promote_`、`_memory_consolidate_`
- `memory/memory.md` 属于 agent 静态背景提示，不受这些环境变量控制，也不等同于 runtime memory store
- `Learn(...)` 成功后会自动执行一轮轻量 observation consolidate；显式 `_memory_consolidate_` 仍用于人工触发完整整理

### Chat Storage 行为调优

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `CHAT_STORAGE_K` | `20` | `Advanced / operator` | query 时回放的历史 run 数量窗口 |

## 排障变量

### SSE / H2A Render

`AGENT_SSE_INCLUDE_TOOL_PAYLOAD_EVENTS` 和 `AGENT_SSE_INCLUDE_DEBUG_EVENTS` 会出现在 `.env.example` 中，方便最终用户发现和按需启用 payload/debug 实时事件；其余 `AGENT_SSE_*` / `AGENT_H2A_RENDER_*` 调优项仍只保留在本参考文档中。

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `AGENT_SSE_INCLUDE_TOOL_PAYLOAD_EVENTS` | `true` | `Debug / troubleshooting` | 是否把工具 payload 事件直接透传到 SSE |
| `AGENT_SSE_INCLUDE_DEBUG_EVENTS` | `false` | `Debug / troubleshooting` | 是否把 `debug.preCall` / `debug.postCall` 暴露给实时流客户端 |
| `AGENT_SSE_HEARTBEAT_INTERVAL_MS` | `15000` | `Debug / troubleshooting` | SSE heartbeat 间隔 |
| `AGENT_H2A_RENDER_FLUSH_INTERVAL_MS` | `0` | `Debug / troubleshooting` | H2A render 定时 flush 间隔；`0` 表示逐事件 flush |
| `AGENT_H2A_RENDER_MAX_BUFFERED_CHARS` | `0` | `Debug / troubleshooting` | H2A render 最大缓冲字符数 |
| `AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS` | `0` | `Debug / troubleshooting` | H2A render 最大缓冲事件数 |
| `AGENT_H2A_RENDER_HEARTBEAT_PASS_THROUGH` | `true` | `Debug / troubleshooting` | 是否透传 heartbeat 事件 |

若现场看起来不像真流式，优先检查是否开启了 `AGENT_H2A_RENDER_FLUSH_INTERVAL_MS`、`AGENT_H2A_RENDER_MAX_BUFFERED_CHARS`、`AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS` 这类传输层缓冲参数；默认 SSE writer 会逐事件 flush。

### Logging

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `LOGGING_AGENT_REQUEST_ENABLED` | `true` | `Debug / troubleshooting` | 是否记录请求到达和完成日志 |
| `LOGGING_AGENT_AUTH_ENABLED` | `true` | `Debug / troubleshooting` | 是否记录鉴权相关日志 |
| `LOGGING_AGENT_EXCEPTION_ENABLED` | `true` | `Debug / troubleshooting` | 是否记录异常日志 |
| `LOGGING_AGENT_TOOL_ENABLED` | `true` | `Debug / troubleshooting` | 是否记录 tool 调用日志 |
| `LOGGING_AGENT_ACTION_ENABLED` | `true` | `Debug / troubleshooting` | 是否记录 action 日志 |
| `LOGGING_AGENT_VIEWPORT_ENABLED` | `true` | `Debug / troubleshooting` | 是否记录 viewport 日志 |
| `LOGGING_AGENT_SSE_ENABLED` | `false` | `Debug / troubleshooting` | 是否记录 SSE 事件日志 |
| `LOGGING_AGENT_MEMORY_ENABLED` | `true` | `Debug / troubleshooting` | 是否启用 memory 独立操作日志 |
| `LOGGING_AGENT_MEMORY_FILE` | `runtime/logs/memory.log` | `Debug / troubleshooting` | memory 独立日志文件路径 |
| `LOGGING_AGENT_LLM_INTERACTION_ENABLED` | `true` | `Debug / troubleshooting` | 是否记录 LLM provider 原始 chunk 与解析后的 delta 日志 |
| `LOGGING_AGENT_LLM_INTERACTION_MASK_SENSITIVE` | `false` | `Debug / troubleshooting` | 是否把 LLM 交互日志替换为 `[masked chars=N]` |

说明：

- 默认日志会直接打印真实 `raw_chunk`、`parsed_content`、`parsed_finish_reason` 和 `parsed_tool_call` 内容
- Bearer token / `apiKey` / `secret` 一类敏感串仍会被替换为 `[redacted]`
- 日志保持单行格式，换行会被转义为 `\n`
- memory 操作日志会单独写入 `LOGGING_AGENT_MEMORY_FILE`，默认不混入主 stdout/stderr

### WebSocket

`AGENT_WS_ENABLED` 默认开启，因此不再出现在 `.env.example` 中；其余 `AGENT_WS_*` 调优项仍只保留在本参考文档中。

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `AGENT_WS_ENABLED` | `true` | `Advanced / operator` | 是否启用 WebSocket 接口与 `/ws` 路由 |
| `AGENT_WS_MAX_MESSAGE_SIZE` | `1048576` | `Advanced / operator` | 单条消息最大字节数 |
| `AGENT_WS_PING_INTERVAL_MS` | `30000` | `Advanced / operator` | ping 心跳间隔 |
| `AGENT_WS_WRITE_TIMEOUT_MS` | `15000` | `Advanced / operator` | 写超时 |
| `AGENT_WS_WRITE_QUEUE_SIZE` | `256` | `Advanced / operator` | 每连接写队列大小 |
| `AGENT_WS_MAX_OBSERVES_PER_CONN` | `8` | `Advanced / operator` | 每连接最大 observe 数 |

说明：

- 默认会注册 `/ws`；仅当 `AGENT_WS_ENABLED=false` 时关闭
- WebSocket handler 会复用当前 catalog、chat、query、run stream 等接口能力
- 鉴权开启时，WebSocket 也会走相同的 token 校验链路

### Reverse WebSocket Gateway

反向 WebSocket 用于让 `agent-platform` 主动连出到一个上游智能体网关；连接建立后，网关会像普通 `/ws` client 一样发送 `request` 帧，`agent-platform` 继续返回 `response` / `stream`，并复用当前 `broadcast()` 推送 `push` 事件。

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `AGENT_GATEWAY_WS_URL` | 空 | `Advanced / operator` | 反向 WebSocket 网关地址；空字符串表示禁用 |
| `AGENT_GATEWAY_WS_TOKEN` | 空 | `Advanced / operator` | 握手时写入 `Authorization: Bearer <token>` 的凭据 |
| `AGENT_GATEWAY_WS_HANDSHAKE_TIMEOUT_MS` | `10000` | `Advanced / operator` | 反向连接握手超时 |
| `AGENT_GATEWAY_WS_RECONNECT_MIN_MS` | `1000` | `Advanced / operator` | 最小重连退避 |
| `AGENT_GATEWAY_WS_RECONNECT_MAX_MS` | `30000` | `Advanced / operator` | 最大重连退避 |

说明：

- 反向连接同样受 `AGENT_WS_ENABLED` 总开关影响；只有 `AGENT_WS_ENABLED=true` 且 `AGENT_GATEWAY_WS_URL` 非空时才会启动
- 握手只使用 `Authorization` header 传递 Bearer token，不会把凭据放进 URL query
- 建连成功后，网关端会先收到一条 `push.connected`
- 断线重连期间的 `broadcast` 为有损投递，当前不提供离线缓冲
- `AGENT_WS_WRITE_TIMEOUT_MS`、`AGENT_WS_WRITE_QUEUE_SIZE`、`AGENT_WS_MAX_MESSAGE_SIZE`、`AGENT_WS_MAX_OBSERVES_PER_CONN` 这些现有 `AGENT_WS_*` 参数同样适用于反向连接

## 不建议公开暴露的变量

### Memory: 已接线但不建议作为公开能力

以下变量当前会被读取到配置中，但尚未形成稳定、清晰的终端用户能力；对于普通部署者而言，不建议暴露：

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `AGENT_MEMORY_HYBRID_VECTOR_WEIGHT` | `0.7` | `Wired but not recommended for public use` | 当前 Go runner 尚未把该权重稳定接入可感知的向量/FTS 混合检索结果 |
| `AGENT_MEMORY_HYBRID_FTS_WEIGHT` | `0.3` | `Wired but not recommended for public use` | 同上 |
| `AGENT_MEMORY_DUAL_WRITE_MARKDOWN` | `true` | `Wired but not recommended for public use` | 当前主要是配置预留，不建议作为产品开关公开 |
| `AGENT_MEMORY_EMBEDDING_PROVIDER_KEY` | 空 | `Wired but not recommended for public use` | 当前主要是配置预留，不建议作为产品开关公开 |
| `AGENT_MEMORY_EMBEDDING_MODEL` | 空 | `Wired but not recommended for public use` | 当前主要是配置预留，不建议作为产品开关公开 |
| `AGENT_MEMORY_EMBEDDING_DIMENSION` | `1024` | `Wired but not recommended for public use` | 当前主要是配置预留，不建议作为产品开关公开 |
| `AGENT_MEMORY_EMBEDDING_TIMEOUT_MS` | `15000` | `Wired but not recommended for public use` | 当前主要是配置预留，不建议作为产品开关公开 |
| `AGENT_MEMORY_AUTO_REMEMBER_ENABLED` | `true` | `Wired but not recommended for public use` | 当前主要作为运行时默认开关；如需禁用自动学习可显式设为 `false` |
| `AGENT_MEMORY_REMEMBER_MODEL_KEY` | 空 | `Wired but not recommended for public use` | 当前主要是配置预留，不建议作为产品开关公开 |
| `AGENT_MEMORY_REMEMBER_TIMEOUT_MS` | `60000` | `Wired but not recommended for public use` | 当前主要是配置预留，不建议作为产品开关公开 |

### Chat Storage: 实现细节变量

以下变量当前更偏内部实现细节或兼容预留，不建议作为最终用户入口：

| 环境变量 | 默认值 | 标签 | 说明 |
|---|---|---|---|
| `CHAT_STORAGE_CHARSET` | `UTF-8` | `Internal / compatibility` | 当前主要是配置预留 |
| `CHAT_STORAGE_ACTION_TOOLS` | 空 | `Internal / compatibility` | 当前主要是配置预留 |
| `CHAT_STORAGE_INDEX_SQLITE_FILE` | `chats.db` | `Internal / compatibility` | 当前 chat store 实际索引文件名 |
| `CHAT_STORAGE_INDEX_AUTO_REBUILD_ON_INCOMPATIBLE_SCHEMA` | `true` | `Internal / compatibility` | 当前主要是配置预留 |

## Provider `apiKey` AES 支持

provider registry 中的 `apiKey` 支持以下两种形态：

- 明文：`apiKey: sk-...`
- 密文：`apiKey: AES(...)`

当值为 `AES(...)` 时，runner 会在加载 `REGISTRIES_DIR/providers` 时自动解密，然后再把真实 key 用于请求上游模型。

说明：

- 解密依赖两部分材料：程序内置 code part + 环境变量 `PROVIDER_APIKEY_KEY_PART`
- 只要命中 `AES(...)`，缺少环境变量或密钥不匹配都会导致 provider 加载失败
- 明文 `apiKey` 仍然兼容
- 旧 `AES(v1:...)` 已不再支持，需要重新生成密文
- 这是“弱对抗”方案，只适合防直接查看配置文件，不等同于完整 secret manager

## `configs/` 目录

当前仓库保留以下模板文件：

- `configs/bash.example.yml`
- `configs/container-hub.example.yml`
- `configs/cors.example.yml`
- `configs/local-public-key.example.pem`

当前真正会被 Go runner 读取的文件：

- `configs/bash.yml`
- `configs/container-hub.yml`
- `configs/cors.yml`
- `configs/local-public-key.pem`

说明：

- `cors.yml` 会直接驱动 `/api/**` 的 CORS 行为
- `local-public-key.pem` 会在启用 `AGENT_AUTH_ENABLED=true` 且使用本地公钥模式时参与 JWT 验签
- 当前 Go 版仍不支持 `CONFIGS_DIR`，配置目录固定为项目根下 `configs/`

## 配置优先级

当前优先级规则为：

```text
代码默认值 < configs/*.yml < 环境变量
```

其中：

- 若 `configs/bash.yml` 或 `configs/container-hub.yml` 不存在，则仍可完全依赖默认值和环境变量
- 环境变量始终优先于 yml

## 当前运行时支持的环境变量族

以下变量族当前都已接入 Go runner；其中只有一部分会默认出现在 `.env.example` 中：

- `AGENT_AUTH_*`
- `CHAT_IMAGE_TOKEN_*`
- `AGENT_CONTAINER_HUB_*`
- `AGENT_DEFAULT_*`
- `AGENT_SCHEDULE_*`
- `AGENT_BASH_*`
- `AGENT_BASH_HITL_*`
- `AGENT_MEMORY_*`
- `CHAT_STORAGE_*`
- `LOGGING_AGENT_*`
- `AGENT_SSE_*`
- `AGENT_H2A_RENDER_*`
- `AGENT_WS_*`

## 当前仍会启动失败的旧变量

- `CONFIGS_DIR`
- `RUNTIME_DIR`
- `AGENT_MEMORY_STORAGE_DIR`
- `MEMORY_CHATS_*`
- 旧 `*_EXTERNAL_DIR`

## Compose 目录映射

`compose.yml` 会把宿主机目录映射到容器内固定路径：

| 宿主机变量 | 容器内路径 |
|---|---|
| `REGISTRIES_DIR` | `/opt/registries` |
| `OWNER_DIR` | `/opt/owner` |
| `AGENTS_DIR` | `/opt/agents` |
| `TEAMS_DIR` | `/opt/teams` |
| `ROOT_DIR` | `/opt/root` |
| `SCHEDULES_DIR` | `/opt/schedules` |
| `CHATS_DIR` | `/opt/chats` |
| `MEMORY_DIR` | `/opt/memory` |
| `PAN_DIR` | `/opt/pan` |
| `SKILLS_MARKET_DIR` | `/opt/skills-market` |

这些环境变量在容器内也会同步设置为对应 `/opt/*` 路径，且 `SERVER_PORT` 固定为 `8080`。

## Agent Context Tags

`context tags` 不是全局默认集合，而是每个 agent 从以下字段读取：

- 优先 `contextConfig.tags`
- 回退 `contextTags`

当前支持/归一化后的标签：

- `system`
- `context`
- `owner`
- `auth`
- `all-agents`
- `memory`

兼容别名映射：

- `agent_identity` / `run_session` / `scene` / `references` / `execution_policy` / `skills` -> `context`
- `memory_context` -> `memory`

说明：

- `context` 负责暴露运行上下文与 sandbox 路径，例如 `sandbox_owner_dir=/owner`、`sandbox_memory_dir=/memory`
- `owner` 负责注入 `OWNER_DIR` 下的 markdown 内容
- `memory` 负责注入运行期 memory context
- `sandbox` 不再属于 `context tags`；只要 agent 配置了 `sandboxConfig`，运行时就会自动注入 sandbox context
