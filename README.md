# agw-springai-agent

本仓库是可独立构建和部署的 Spring AI Agent 服务，已经改为直接引用仓库内的 SDK jar，不依赖本地 Maven 安装。

## 提供接口

- `POST /api/agent/{agentId}`: OpenAI chunk SSE
- `POST /api/agw-agent/{agentId}`: AGW 标准 SSE

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

## Docker Compose 部署（外部目录）

请在仓库外（例如 `/opt/1panel/docker/compose/agw-springai-agent`）维护 `docker-compose.yml`，示例：

```yaml
services:
  springboot:
    build: ./agw-springai-agent
    container_name: agw-springai-agent
    ports:
      - 9904:8080
    environment:
      AGENT_EXTERNAL_DIR: /opt/agents
      AGENT_REFRESH_INTERVAL_MS: 10000
      SPRING_CONFIG_ADDITIONAL_LOCATION: optional:file:/opt/application.yml
      OPENAI_API_KEY: ${OPENAI_API_KEY}
    volumes:
      - ./application.yml:/opt/application.yml:ro
      - ./agents:/opt/agents:ro
```

启动：

```bash
docker compose up -d --build
```

查看日志：

```bash
docker compose logs -f
```

## settings.xml 说明

- `settings.xml` 作为构建镜像时 Maven 配置，会被 `Dockerfile` 拷贝到 Maven 全局配置目录。
- 当前配置使用 `central` 的阿里云镜像，加速依赖下载。

## agents 目录

- `agents/*.json` 文件名（不含 `.json`）即 `agentId`
- 服务启动时会先加载一次，之后每 10 秒刷新一次缓存（默认值）
- 可通过 `AGENT_EXTERNAL_DIR` 指定目录，通过 `AGENT_REFRESH_INTERVAL_MS` 调整刷新间隔
