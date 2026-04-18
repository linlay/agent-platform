# Agent Definition 参考

本文件与参考仓库保持同一主题。当前 Go runner 已支持从 `AGENTS_DIR` 读取 `runtime/agents/*.yml` 与 `runtime/agents/<key>/agent.yml`，并将结果直接暴露给 `/api/agents` 与 query agent 选择逻辑。

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
  tools: ["_bash_", "_datetime_"]
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

- `ONESHOT / REACT / PLAN_EXECUTE` 的完整定义驱动编排
- prompt 分层拼装
- per-agent memory / skill / tool 目录覆盖
- WatchService 类文件系统事件热重载

## Context Tags

当前 Go runner 不会为所有 agent 自动附加一组全局默认 `context tags`；每个 agent 仍需在 definition 中显式声明：

- 优先 `contextConfig.tags`
- 回退 `contextTags`

支持/归一化后的标签：

- `system`
- `context`
- `owner`
- `auth`
- `sandbox`
- `all-agents`
- `memory`

兼容别名映射：

- `agent_identity` / `run_session` / `scene` / `references` / `execution_policy` / `skills` -> `context`
- `memory_context` -> `memory`

其中：

- `context` 会暴露运行时上下文与 sandbox 路径
- `owner` 会注入 `OWNER_DIR` 下的 markdown 内容
- `memory` 会注入运行期 memory context

## Sandbox Config

Go runner 当前支持在 `agent.yml -> sandboxConfig` 下声明：

```yaml
sandboxConfig:
  environmentId: shell
  level: RUN
  env:
    HTTP_PROXY: "http://127.0.0.1:7890"
    HTTPS_PROXY: "http://127.0.0.1:7890"
    TZ: "Asia/Shanghai"
```

约束与语义：

- `env` 只接受 `map[string]string`
- key 必须非空，且不能包含空白字符或 `=`
- value 必须是字面量字符串；空字符串允许并原样下发
- 不支持 `${VAR}` 或其他宿主环境变量展开
- agent `sandboxConfig.env` 作为基础值，skill 目录下的 `.sandbox-env.json` 会按 agent 声明顺序叠加并覆盖同名键
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
