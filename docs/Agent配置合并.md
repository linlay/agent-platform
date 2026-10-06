# Agent 配置合并

## 配置归属

- `configs/agent-settings.yml`：全局及 mode 预置、创建默认值、Workspace 规则文件、顶层 ACP bridges。
- `configs/agent-prompt.yml`：`shared`（runtime/skill/tool-appendix/plan-execute/btw）、`coder`、`kbase` 提示词。注入时机不变。
- `configs/tools.yml`：访问策略、Bash/FileTools/run-env/runQuery，以及顶层 vision-recognize/web-fetch/image-generate。AI profile 的 system-prompt 随 profile 保存。
- `configs/runtime.yml`：平台运行设置、`kbx.embedding`、`memory`。memx 继续由 memory.worker 配置，不新增 memx 节。

所有上述 YAML 在启动时读取，修改需要重启；注册表连接快照可在调用前重新同步。

## Agent 设置

agent-settings 顶层 preset-tools/preset-connectors 与 general/coder/kbase 各自的同名数组相加，按名称去重并保留首次出现顺序。mode 省略或 [] 仅表示无增量，不清除全局项。预置仍只作用于 native GENERAL/CODER/KBASE，不写入 Agent YAML。创建、编辑、展示、装配和连接器删除保护使用相同有效预置；已有显式声明不被删除。

分发示例通过 `coder.preset-tools` 为普通 CODER 提供 `regex`，CODER mode profile 的缺省工具列表不再内置该工具。

`<mode>.default-agent.modelKey/reasoningEffort` 只在创建时补全，已有 Agent 不受动态覆盖。GENERAL/CODER 的 budget 语义不变。kbase 节表示 KBASE Agent，不是知识库引擎。

GENERAL/CODER 的 `workspace-agents` 仅接受非空 `file`：声明即自动读取，省略整个节点即不读取；enabled 已删除。不自动读取不限制工具按权限主动读文件。GENERAL 无具体 Workspace 或使用 @root 时不读取；CODER 显式 projectConfig.promptFiles 仍优先。示例仅为 CODER 声明 AGENTS.md。

`acp-bridges` 位于顶层，配置类型独立于 CODER。Desktop 注册仅修改目标 bridge，保留其他节点、环境变量表达式及插件归属；仍需重启激活。现有 ACP mode 准入未扩展。

## KBX 与旧 KBASE 下线

```yaml
kbx:
  embedding:
    model-key:
    prompt: raw # raw 或 qwen3
```

Platform 从 runtime.yml 选择模型并解析模型注册表。知识库中心与 Agent capability 共用部署级 ModelConfigSource，受管快照位于 `<AP_RUNTIME_STATE_DIR>/kbx/index.yml`，通过显式 --config 选择，不依赖 KBX_CONFIG_FILE。Agent 调用保留内容范围、过滤和切块参数，派生配置的模型段只能来自该快照。普通工具不能读取受管 StateDir。

空 model-key 关闭向量配置，不回退聊天模型；非空无效模型明确失败。每次同步重新解析注册表，仅连接快照变化才写入文件。KBX 负责模型/prompt 与索引契约检查；不可用或维度不匹配显式报告，不自动执行 embed --force，也不自动迁移已有索引。配置同步不意味着索引已经兼容新模型。

Agent YAML 的 kbaseConfig.embedding 已退役，出现即报错；创建流程不再补入该字段。旧 index/maintenance/refresh/extraction 从生产配置入口与模板下线，不转换为 KBX 参数；旧引擎代码和已有索引暂留。Agent capability 的索引维护协议尚未接通，此次配置统一不改变该限制。

## 离线迁移

先停止使用该部署的 Platform 进程。以下 config-dir 指包含 configs/ 的部署根；agents-dir 可选，传入可编辑 Agent 源目录，不是 ru-agents。

```bash
./agent-platform config-migrate --config-dir /path/to/deployment --agents-dir /path/to/runtime/agents
./agent-platform config-migrate --config-dir /path/to/deployment --agents-dir /path/to/runtime/agents --apply
```

默认只输出路径和退役项，不输出配置值。显式 apply 在全部候选校验通过后写入，备份在 `<config-dir>/config-backups/<timestamp>/`，restore.tsv 记录原文件对应关系；NEW 行表示迁移新建的文件，恢复时删除这些新文件。迁移发生写入失败时尝试恢复，任何恢复失败均报告并保留备份。

旧 general-settings/coder-settings/kbase-settings 按 mode 合并；prompts/coder-prompts/kbase-prompts 按 shared/coder/kbase 合并；tools 的 preset 移入 agent-settings，ai-tools 有效配置移入 tools。旧 KBASE 引擎设置不复制，原值保存在备份。未接入的禁用 speech 示例移除，启用 speech 时要求先处理。新旧目标节点冲突直接停止，不决定覆盖顺序。

指定 agents-dir 时检查并移除 Agent embedding 声明；与旧全局模型不同的声明导致冲突，必须先明确统一的模型选择。未指定时不改 Agent 文件，运行时忽略遗留 embedding 声明，统一使用 runtime.kbx.embedding。不会访问或重建索引。

启动和部署忽略七个旧配置文件，不再因其存在而失败；旧文件内容不参与配置加载，保留定制值需显式迁移。tools 中的旧 preset 位置仍需迁入 agent-settings。部署参数 --ai-* 写入 tools，--coder-* 和 --kbase-model-key/--kbase-reasoning-effort 写入 agent-settings，--kbase-embedding-model-key 写入 runtime.kbx.embedding.model-key；仅首次生成时渲染，已有文件不覆盖。

## Runtime Context 语言与模板

环境头部配置位于 `agent-prompt.yml -> shared.runtime`，示例见 `configs/agent-prompt.example.yml`。只有两个设置：

- `default-locale`：无客户端语言时使用，缺省 `zh-CN`；支持现有语言归一化规则，例如 `en-US` 归为 `en`。
- `environment-prompt-template`：环境头部模板，只接受 `{{os}}`、`{{arch}}`、`{{timezone}}`、`{{locale}}` 四个占位符，可在双大括号内留空格。

省略设置使用默认值；显式空白、null、错误类型、不支持的语言、未知或残缺占位符均报错。已有配置文件 YAML 损坏会阻止启动。目录字段由运行时在模板之后追加，说明固定为英文；模板不负责目录布局。是否注入环境段仍由 Agent 的 `contextConfig.tags` 中是否包含 `system` 控制。

Desktop 的全局语言仍通过连接握手和 `/api/locale` 同步，不增加任何业务请求字段。新的 Native Run 从当前客户端语言生成提示词；无语言头的 HTTP 请求和 Automation 使用上述默认值。子智能体、Team 成员及 `chat_start` 继承父 Run 的提示词语言。CODER/KBASE/TEAM 的 `language_preference` 与环境段使用同一归一化语言。

Run 首次准备的语言、环境模板及各阶段 system-init 存于受保护的 `<StateDir>/run-prompts/`，沿用现有 Run 私有快照的写入方式。普通工具循环复用已生成的消息；同 Run 的 submit、跨设备恢复及 wait 重启恢复读取快照，不按提交客户端语言重新渲染。Planning 转执行复用已准备的执行阶段；若创建新的执行 Run，则继承源 Run 的语言。快照损坏报错，不静默重建；旧 Run 没有这种快照时沿用原准备路径，不改写历史数据。

新 Run 仍通过既有 system-init 内容比较决定是否复用。指纹覆盖实际渲染内容、工具和请求配置；语言相同但模板、上下文等内容变化也会产生新版本。历史回放与导出使用精确 systemRef；遇到旧数据中同一引用对应不同内容时明确报错，不猜测版本。

升级后默认环境文字由“中文”改为 `zh-CN`，目录说明改为英文，相关 Chat 的下一次 Run 会生成新的 system-init；未包含环境段的提示词仍可能因完整内容指纹调整产生新记录。语言来回切换也会切换提示词版本，可能影响供应商缓存。自定义 CODER/KBASE 模板的 `language_preference` 现在得到 `en` / `zh-CN`，部署应检查相关措辞。此改动仅作用于 Platform 生成的 Native 提示词，不改变外部 ACP bridge 的提示词管理。
