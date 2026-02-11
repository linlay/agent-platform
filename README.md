# agw-springai-agent

本仓库是可独立构建和部署的 Spring AI Agent 服务，已经改为直接引用仓库内的 SDK jar，不依赖本地 Maven 安装。

## 提供接口

- `GET /api/agents`: 智能体列表
- `GET /api/agent?agentKey=...`: 智能体详情
- `POST /api/query`: 提问接口（默认返回 AGW 标准 SSE；`requestId` 可省略，缺省时等于 `runId`）
- `POST /api/submit`: Human-in-the-loop 提交接口
- `POST /raw-api/{agentId}`: 原始 OpenAI chunk SSE（调试接口）

## 返回格式约定

- `POST /api/query` 返回 SSE event stream。
- 其它 JSON 接口统一返回：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

- `code = 0` 表示成功，`code > 0` 表示失败（整型错误码），`msg` 为错误信息，`data` 为返回数据。
- `data` 直接放业务内容，不再额外包同名字段，例如：
  - 智能体列表：`data` 直接是 `agents[]`
  - 智能体详情：`data` 直接是 `agent`

## 目录约定

```text
.
├── libs/
│   └── agw-springai-sdk-0.0.1-SNAPSHOT.jar
├── src/
├── agents/
├── pom.xml
├── settings.xml
└── Dockerfile
```

## SDK jar 放置方式

从旁边的 `agw-springai-sdk` 项目构建后，将 jar 放到本仓库 `libs/`：

```bash
cp ../agw-springai-sdk/target/agw-springai-sdk-0.0.1-SNAPSHOT.jar ./libs/
```

`pom.xml` 已固定通过 `systemPath` 引用 `libs/agw-springai-sdk-0.0.1-SNAPSHOT.jar`，可以直接提交到 Git。

## 本地运行

```bash
mvn clean test
mvn spring-boot:run
```

默认端口 `8080`。

## 接口测试用例

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"给我一个微服务网关的落地方案，100字内","agentKey":"demoPlanExecute"}'
```

## settings.xml 说明

- `settings.xml` 作为构建镜像时 Maven 配置，会被 `Dockerfile` 拷贝到 Maven 全局配置目录。
- 当前配置使用 `central` 的阿里云镜像，加速依赖下载。

## agents 目录

- `agents/*.json` 文件名（不含 `.json`）即 `agentId`
- 服务启动时会先加载一次，之后每 10 秒刷新一次缓存（默认值）
- 可通过 `AGENT_EXTERNAL_DIR` 指定目录，通过 `AGENT_REFRESH_INTERVAL_MS` 调整刷新间隔
- `systemPrompt` 同时支持标准 JSON 字符串和 `"""` 多行写法（仅 `systemPrompt`）

标准 JSON：

```json
{
  "description": "算命大师",
  "providerType": "BAILIAN",
  "model": "qwen3-max",
  "systemPrompt": "你是算命大师",
  "mode": "PLAIN",
  "tools": []
}
```

多行写法：

```json
{
  "description": "算命大师",
  "providerType": "BAILIAN",
  "model": "qwen3-max",
  "systemPrompt": """
你是算命大师
请先问出生日期
""",
  "mode": "RE_ACT",
  "tools": ["bash", "city_datetime"]
}
```

`mode` 支持：
- `PLAIN`（默认直答；当配置了 tools 时，会先决策是否调用工具，最多调用 1 次）
- `RE_ACT`（兼容旧值 `THINKING_AND_CONTENT`）
- `PLAN_EXECUTE`（兼容旧值 `THINKING_AND_CONTENT_WITH_DUAL_TOOL_CALLS`）

当 `tools` 非空时，服务会按 OpenAI 兼容的原生 Function Calling 协议请求模型：
- 请求体包含 `tools[]`
- 流式消费 `delta.tool_calls`
- 不再依赖正文中的 `toolCall/toolCalls` JSON 字段（仍保留向后兼容解析）

## 内置 agentCreator 智能体

- 内置 `agentCreator` 智能体，模式为 `PLAN_EXECUTE`
- 内置真实工具 `agent_file_create`，用于创建/更新 `agents/{agentId}.json`
- 建议在请求中提供：`agentId`、`description`、`systemPrompt`、`providerType`、`model`、`deepThink`
- 工具会校验 `agentId`（仅允许 `A-Za-z0-9_-`，最长 64）
- `providerType` 不做白名单校验；未提供时默认 `BAILIAN`
- 生成格式：

```json
{
  "description": "算命大师",
  "providerType": "BAILIAN",
  "model": "qwen3-max",
  "systemPrompt": "你是算命大师",
  "deepThink": false
}
```

- `systemPrompt` 为多行时会写成 `"""` 形式

## Bash 工具目录授权

`bash` 工具默认仅允许访问工作目录（`user.dir`）。若需要让 Agent 在容器内读取 `/opt` 等目录，可配置：

```yaml
agent:
  tools:
    bash:
      working-directory: /opt/app
      allowed-paths:
        - /opt
```

也可使用环境变量：

```bash
AGENT_BASH_WORKING_DIRECTORY=/opt/app
AGENT_BASH_ALLOWED_PATHS=/opt,/data
```
