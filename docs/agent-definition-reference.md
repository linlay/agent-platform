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
  backends: ["_bash_", "_datetime_"]
  frontends: []
  actions: []
mode: ONESHOT
plain:
  systemPrompt: |
    系统提示词
```

长期看，Go 版也会沿用这一思路：

- 前 4 行固定 `key`、`name`、`role`、`description`
- `modelConfig` 引用 `registries/models/*.yml`
- `toolConfig` 声明 backend / frontend / action 工具
- `mode` 决定 agent 执行模式

## Go 版已落地能力

- agent YAML 文件解析
- team YAML 文件解析
- skill prompt 目录化装载与 `/api/skills`
- 默认 agentKey 由真实 registry 暴露
- query 请求会绑定 `agentKey`
- tool 执行器已支持 backend tool
- Container Hub sandbox 已可参与 tool 执行
- model registry 已支持从外部目录读取模型定义
- 定时轮询式 catalog/model refresh

## Go 版暂未落地能力

- `ONESHOT / REACT / PLAN_EXECUTE` 的完整定义驱动编排
- prompt 分层拼装
- context tags
- per-agent memory / skill / tool 目录覆盖
- WatchService 类文件系统事件热重载

## 当前建议

当前建议：

- 把 `runtime/agents` / `runtime/teams` / `runtime/skills-market` 视为事实源
- 新增 agent 行为时优先修改 definition 文件，其次再补 `internal/catalog` 与 `internal/engine`
- 若某个字段尚未在 Go 中消费，保持显式报错或显式忽略，避免静默吞掉

## 后续迁移原则

- 文件命名尽量与参考仓库保持一致
- 语义保持兼容优先于实现方式兼容
- 未实现的字段要么明确忽略，要么启动时报错，避免静默吞掉
