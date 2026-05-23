# Agent Definition 参考

本文件与参考仓库保持同一主题。当前 Go runtime 已支持从 `AGENTS_DIR` 读取 `runtime/agents/*.yml` 与 `runtime/agents/<key>/agent.yml`，并将结果直接暴露给 `/api/agents` 与 query agent 选择逻辑。

## 当前状态

当前 Go 版运行时：

- `models` 会从 `REGISTRIES_DIR/models` 读取
- provider 连接配置会从 `REGISTRIES_DIR/providers` 读取
- catalog 中的 agent/team/skill/tool 已改为目录驱动
- `runtime/agents/*.yml` 与 `runtime/agents/<key>/agent.yml` 都会参与装载

因此，这份文档当前主要承担两件事：

1. 说明与 Java 对齐后的 Agent Definition 形态
2. 标注当前 Go 版已接入与仍待补齐的细节

## 目标形态

参考仓库中的完整 Agent Definition 目标形态如下：

```yaml
key: agent_key
name: agent_name
role: 角色标签
description: 描述
icon: "emoji:🤖"
modelConfig:
  modelKey: qwen3-max
toolConfig:
  tools: ["bash", "datetime"]
mode: ONESHOT
plain:
  systemPrompt: |
    系统提示词
```

长期看，Go 版也会沿用这一思路：

- 前 4 行固定 `key`、`name`、`role`、`description`
- `modelConfig` 引用 `registries/models/*.yml`
- `toolConfig` 直接声明工具名列表
- `mode` 决定 agent 执行模式

## Agent Identity 与 SOUL.md 边界

`agent.yml` 是 agent 身份与能力元数据的唯一事实源：

- `key`
- `name`
- `role`
- `description`
- `mode`
- `toolConfig`
- `skillConfig`
- `runtimeConfig`

运行时会先根据 `agent.yml` 生成统一的 `Agent Identity` prompt section，再拼接其他 prompt 层。

`SOUL.md` 的职责是长期行为提示，不是配置副本：

- 允许写人格、协作方式、风险姿态、硬边界、非目标
- 允许写长期稳定的行为约束
- 不应重复 `key/name/role/description/mode`
- 不应复制 tools、skills、sandbox、环境路径或一次性任务说明

推荐结构：

```md
# Soul

## Persona

## Boundaries

## Working Style

## Long-Term Notes
```

## Go 版已落地能力

- agent YAML 文件解析
- team YAML 文件解析
- skill prompt 目录化装载与 `/api/skills`
- 默认 agentKey 由真实 registry 暴露
- query 请求会绑定 `agentKey`
- tool 执行器已支持 backend tool
- Container Hub sandbox 已可参与 tool 执行
- context tags 解析、归一化与 prompt 注入
- model registry 已支持从外部目录读取模型定义
- 定时轮询式 catalog/model refresh

## Go 版暂未落地能力

- `REACT / CODER / PLAN_EXECUTE / PROXY` 的完整定义驱动编排；`ONESHOT` 作为旧兼容值保留
- prompt 分层拼装
- per-agent memory / skill / tool 目录覆盖
- WatchService 类文件系统事件热重载

## Context Tags

当前 Go runtime 不会为所有 agent 自动附加一组全局默认 `context tags`；每个 agent 只从 `contextConfig.tags` 读取声明：

```yaml
contextConfig:
  tags:
    - system
    - session
```

支持标签：

- `system`
- `session`
- `owner`
- `all-agents`

其中：

- `session` 会暴露运行时上下文
- `owner` 会注入 `OWNER_DIR` 下的 markdown 内容；`mode: CODER` 不会默认启用，需要显式声明
- `sandbox` 不再通过 `context tags` 控制；只要 agent 声明了 `runtimeConfig.environmentId`，运行时会自动注入 sandbox context
- runtime memory context 不再通过 `context tags` 控制；只有 agent 显式开启 `memoryConfig.enabled: true` 时，运行时才会自动注入 memory context

## Prompt Files

目录式 agent 会按约定读取 prompt 文件：

- 普通模式默认读取 `AGENTS.md`
- 顶层 `promptFile` 可显式覆盖普通模式 prompt
- `PLAN_EXECUTE` 默认读取 `AGENTS.plan.md`、`AGENTS.execute.md`、`AGENTS.summary.md`
- `planExecute.<stage>.promptFile` 可显式覆盖对应阶段
- `PLAN_EXECUTE` 阶段缺少约定文件时，先回退顶层 `promptFile`，再回退 `AGENTS.md`
- `mode: CODER` 还会按 `configs/coder-settings.yml` 读取 `runtimeConfig.workspace.root/AGENTS.md`，用于注入项目级 workspace 规则

## CODER Mode

CODER 是一等 agent mode，应写作 `mode: CODER`，不要使用旧的 `type: CODER`。CODER 默认使用 `bash`、`file_read`、`file_write`、`file_edit`、`file_grep`、`datetime`。非沙箱 CODER 要求 `runtimeConfig.workspace.root` 为绝对路径；声明了 `runtimeConfig.environmentId` 的沙箱 CODER 可省略宿主机 workspace。

请求顶层 `planningMode: true` 只对 `mode: CODER` 生效：planning 阶段仅暴露 `file_read`、`file_grep`、`datetime`、`ask_user_question`、`planning_write`；`planning_write` 会把标准 Markdown 规划写入 `CHATS_DIR/plans/<runId>_planning.md` 并触发 live `planning.start` / `planning.delta` / `planning.end` 流事件。live 事件保持轻量：`planning.start` 建立上下文，`planning.delta` 仅携带 `planningId` 与增量文本，`planning.end` 仅携带 `planningId`；`planning.snapshot` 由后端聚合后用于持久化回放和 debug 展示，`planningFile` 只暴露文件名。用户以 approval 确认后，execution 阶段再暴露 CODER 执行工具，不追加 `plan_update_task`。

CODER 专属系统提示词从 `configs/coder-prompts.yml` 读取；当前支持 `planning-prompt`。该文件是运行时配置事实源，提示词正文不在 Go 源码中维护。

## Static Memory 与 Runtime Memory

agent 目录下的 `memory/memory.md` 当前作为静态背景提示装载：

- 语义：agent 的长期固定背景，不是运行时记忆库
- 注入顺序：`Agent Identity` 与 `SOUL.md` 之后、runtime memory bundle 之前
- 存储位置：随 agent 文件存在，不进入 SQLite memory store

运行时记忆来自 memory store：

- `Stable Memory`：长期稳定、可复用的 `fact`
- `Relevant Observations`：近期观察类 `observation`

`snapshot/*.md` 仅作为导出/审阅视图：

- 不参与 prompt 主链路
- 不作为 runtime memory 的事实源

## Memory Tool 注入

agent definition 可通过 `memoryConfig` 控制默认注入的 memory 工具：

```yaml
memoryConfig:
  enabled: true
  managementTools: true
  embedding:
    providerKey: babelark
  autoRemember:
    enabled: true
    modelKey: minimax-m2_7-anthropic
    timeoutMs: 60000
```

规则：

- 未配置 `memoryConfig.enabled` 或配置为 `false` 时，不启用 runtime memory，也不注入 memory tools
- `enabled: true` 时注入基础集：
  - `memory_write`
  - `memory_read`
  - `memory_search`
- `managementTools: true` 时额外注入管理集：
  - `memory_update`
  - `memory_forget`
  - `memory_timeline`
  - `memory_promote`
  - `memory_consolidate`
- `autoRemember.enabled: true` 时，run 完成后自动执行 learn / auto-remember
- `autoRemember.modelKey` 填 `REGISTRIES_DIR/models/*.yml` 中的 model key，用于记忆筛选、总结、合并
- `embedding.providerKey` 填 `REGISTRIES_DIR/providers/*.yml` 中的 provider key；embedding 模型、维度和超时优先来自 provider 的 `memory.embedding`，agent 侧也可覆盖

运行时策略：

- `Learn(...)` 成功后会自动执行一轮轻量 observation 整理
- 自动整理默认只做 stale/duplicate 收口，以及“重复出现 observation”的晋升
- 更激进的生命周期治理仍建议通过显式 `memory_consolidate` 触发

## Runtime Config

Go runtime 当前支持在 `agent.yml -> runtimeConfig` 下声明：

```yaml
runtimeConfig:
  environmentId: shell
  level: RUN
  workspace:
    root: /absolute/project/path
  env:
    HTTP_PROXY: "http://127.0.0.1:7890"
    HTTPS_PROXY: "http://127.0.0.1:7890"
    TZ: "Asia/Shanghai"
```

约束与语义：

- `env` 只接受 `map[string]string`
- key 必须非空，且不能包含空白字符或 `=`
- value 必须是字面量字符串；空字符串允许并原样注入 host bash 或下发到 Container Hub
- 不支持 `${VAR}` 或其他宿主环境变量展开
- `runtimeConfig.workspace.root` 定义宿主机 workspace 边界，供 host bash 与文件工具使用；该字段必须是绝对路径
- `workspaceConfig.root` 已废弃，仅作为旧配置兼容 fallback；新 agent 应使用 `runtimeConfig.workspace.root`
- `runtimeConfig.environmentId` 是 sandbox context 的唯一入口；不需要也不支持再在 `contextConfig.tags` 中声明 `sandbox`
- agent `runtimeConfig.env` 作为基础值，skill 目录下的 `.sandbox-env.json` 会按 agent 声明顺序叠加并覆盖同名键
- `/api/agents` 与 `/api/agent` 的 `sandbox` meta 不会回显 `env`，避免暴露代理地址、凭据或私有 endpoint；`extraMounts` 仍可对外暴露，因为它描述的是白名单路径而非敏感值

## 当前建议

当前建议：

- 把 `runtime/agents` / `runtime/teams` / `runtime/skills-market` 视为事实源
- 新增 agent 行为时优先修改 definition 文件，其次再补 `internal/catalog` 与 `internal/engine`
- 若某个字段尚未在 Go 中消费，保持显式报错或显式忽略，避免静默吞掉

## 后续迁移原则

- 文件命名尽量与参考仓库保持一致
- 语义保持兼容优先于实现方式兼容
- 未实现的字段要么明确忽略，要么启动时报错，避免静默吞掉
