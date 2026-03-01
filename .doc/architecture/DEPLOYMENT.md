# 部署架构与环境

## 启动方式
- 本地：`mvn spring-boot:run`
- 全量测试：`mvn clean test`
- 平台脚本：
  - mac: `release-scripts/mac/*`
  - windows: `release-scripts/windows/*`

## 资源同步与运行目录
应用启动后 `RuntimeResourceSyncService` 会把 classpath 资源同步到外部目录：
- `src/main/resources/agents -> agents/`
- `src/main/resources/models -> models/`
- `src/main/resources/tools -> tools/`
- `src/main/resources/skills -> skills/`
- `src/main/resources/viewports -> viewports/`

同名内置资源会覆盖目标文件；外置自定义资源保留。

## 热加载
`DirectoryWatchService` 监听以下目录变化并触发刷新：
- `agents/` -> `AgentRegistry.refreshAgents()`
- `models/` -> `ModelRegistryService.refreshModels()` + `AgentRegistry.refreshAgents()`
- `tools/` -> `CapabilityRegistryService.refreshCapabilities()`
- `skills/` -> `SkillRegistryService.refreshSkills()`
- `viewports/` -> `ViewportRegistryService.refreshViewports()`

## 核心环境变量
- Server: `SERVER_PORT`
- Agent 目录：`AGENT_EXTERNAL_DIR`
- Model 目录：`AGENT_MODEL_EXTERNAL_DIR`
- Tool 目录：`AGENT_TOOLS_EXTERNAL_DIR`
- Skill 目录：`AGENT_SKILL_EXTERNAL_DIR`
- Viewport 目录：`AGENT_VIEWPORT_EXTERNAL_DIR`
- Data 目录：`AGENT_DATA_EXTERNAL_DIR`
- Auth：`AGENT_AUTH_*`
- Chat image token：`CHAT_IMAGE_TOKEN_*`
- Memory：`MEMORY_CHAT_*`

## 容器与配置文件约定
- `Dockerfile` 在项目根目录。
- `settings.xml` 在项目根目录。
- 本地覆盖配置可放 `application.yml`。
- 生产可通过 `/opt/application.yml` 叠加（见 `spring.config.import`）。

## CORS
默认关闭（`agent.cors.enabled=false`）。
开启后仅作用于 `agent.cors.path-pattern`（默认 `/api/**`）。

## 部署验收清单
1. `agents/models/tools/skills/viewports` 目录存在且可读。
2. `chats.db` 可写，`chats/` 可写。
3. provider key/base-url 配置完整。
4. 鉴权模式（JWT 或 data token）与调用方一致。
