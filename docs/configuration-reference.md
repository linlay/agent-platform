# Configuration 参考

当前 Go runner 的配置事实源主要来自两部分：

- 代码默认值：`internal/config/config.go`
- 外部输入：环境变量与 `configs/*.yml`

## 核心环境变量

### Server

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `HOST_PORT` | `11949` | 面向运维/开发的主端口开关；本地 `make run` 会优先映射到应用监听端口，`compose.yml` 会映射到容器 `8080` |
| `SERVER_PORT` | `8080` | 应用内部监听端口变量；主要作为 `make run` 的回退值与直接 `go run` 的端口来源 |

本地运行约定：

- `make run` 会先加载根目录 `.env`
- `make run` 的端口链路为 `HOST_PORT -> SERVER_PORT -> 8080`
- 直接执行 `go run ./cmd/agent-platform-runner` 不会自动加载 `.env`
- 直接 `go run` 时，应用端口链路仍然是 `SERVER_PORT -> 8080`
- `HOST_PORT` 是日常本地联调和容器化部署共用的主入口端口

### 目录配置

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `REGISTRIES_DIR` | `runtime/registries` | provider / model registry 根目录 |
| `OWNER_DIR` | `runtime/owner` | owner 目录 |
| `AGENTS_DIR` | `runtime/agents` | agents 定义目录，驱动 `/api/agents` 与 query 默认 agent |
| `TEAMS_DIR` | `runtime/teams` | teams 定义目录，驱动 `/api/teams` 与 team 默认 agent |
| `ROOT_DIR` | `runtime/root` | runner 根目录映射 |
| `SCHEDULES_DIR` | `runtime/schedules` | schedules 定义目录 |
| `CHATS_DIR` | `runtime/chats` | chat 落盘目录 |
| `MEMORY_DIR` | `runtime/memory` | remember 落盘目录 |
| `PAN_DIR` | `runtime/pan` | pan 目录映射 |
| `SKILLS_MARKET_DIR` | `runtime/skills-market` | skills market 目录映射 |

### Container Hub

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `AGENT_CONTAINER_HUB_ENABLED` | `false` | 是否启用 Container Hub |
| `AGENT_CONTAINER_HUB_BASE_URL` | `http://127.0.0.1:11960` | Container Hub 服务地址 |
| `AGENT_CONTAINER_HUB_AUTH_TOKEN` | 空 | Bearer token |
| `AGENT_CONTAINER_HUB_DEFAULT_ENVIRONMENT_ID` | 空 | 默认 environment id |
| `AGENT_CONTAINER_HUB_REQUEST_TIMEOUT_MS` | `300000` | 请求超时 |
| `AGENT_CONTAINER_HUB_DEFAULT_SANDBOX_LEVEL` | `run` | 默认沙箱级别 |
| `AGENT_CONTAINER_HUB_AGENT_IDLE_TIMEOUT_MS` | `1800000` | agent 级沙箱闲置回收时间 |
| `AGENT_CONTAINER_HUB_DESTROY_QUEUE_DELAY_MS` | `5000` | 销毁队列延迟 |

### Bash 工具

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `AGENT_BASH_WORKING_DIRECTORY` | `.` | `_bash_` 默认工作目录 |
| `AGENT_BASH_ALLOWED_PATHS` | 空 | 允许访问的路径白名单 |
| `AGENT_BASH_ALLOWED_COMMANDS` | 空 | 允许执行的命令白名单 |
| `AGENT_BASH_PATH_CHECKED_COMMANDS` | 空 | 开启路径校验的命令 |
| `AGENT_BASH_PATH_CHECK_BYPASS_COMMANDS` | 空 | 跳过路径校验的命令 |
| `AGENT_BASH_SHELL_FEATURES_ENABLED` | `false` | shell 高级语法开关 |
| `AGENT_BASH_SHELL_EXECUTABLE` | `bash` | shell 可执行文件 |
| `AGENT_BASH_SHELL_TIMEOUT_MS` | `30000` | shell 模式超时 |
| `AGENT_BASH_MAX_COMMAND_CHARS` | `16000` | 最大命令长度 |

### Logging

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `LOGGING_AGENT_LLM_INTERACTION_ENABLED` | `true` | 是否记录 LLM provider 原始 chunk 与解析后的 delta 日志 |
| `LOGGING_AGENT_LLM_INTERACTION_MASK_SENSITIVE` | `false` | 是否把 LLM 交互日志替换为 `[masked chars=N]`；关闭时输出真实内容，但仍会把 Bearer token / `apiKey` / `secret` 等敏感串替换为 `[redacted]` |

说明：

- 默认日志会直接打印真实 `raw_chunk`、`parsed_content`、`parsed_finish_reason` 和 `parsed_tool_call` 内容，便于排查模型侧是否真的逐 chunk 返回。
- 日志仍保持单行格式，换行会被转义为 `\n`。
- 若现场看起来不像真流式，优先检查是否开启了 `AGENT_H2A_RENDER_FLUSH_INTERVAL_MS`、`AGENT_H2A_RENDER_MAX_BUFFERED_CHARS`、`AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS` 这类传输层缓冲参数；默认 SSE writer 会逐事件 flush。

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
- `AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE` 默认值为 `configs/local-public-key.pem`
- `AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE` 若是绝对路径则原样使用；若是相对路径则按项目根解析
- 为兼容旧写法，`local-public-key.pem` 会自动解析到 `configs/local-public-key.pem`

## 配置优先级

当前优先级规则为：

```text
代码默认值 < configs/*.yml < 环境变量
```

其中：

- 若 `configs/bash.yml` 或 `configs/container-hub.yml` 不存在，则仍可完全依赖默认值和环境变量
- 环境变量始终优先于 yml

## 当前已支持的 Java 环境变量族

以下变量已接入 Go runner，与 Java runner 保持同名语义：

- `AGENT_AUTH_*`
- `CHAT_IMAGE_TOKEN_*`
- `AGENT_SCHEDULE_*`
- `CHAT_STORAGE_*`
- `LOGGING_AGENT_*`
- `AGENT_MEMORY_*`
- `AGENT_DEFAULT_*`
- `AGENT_*_REFRESH_INTERVAL_MS`
- `AGENT_SSE_INCLUDE_TOOL_PAYLOAD_EVENTS`
- `AGENT_SSE_HEARTBEAT_INTERVAL_MS`
- `AGENT_H2A_RENDER_*`

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
