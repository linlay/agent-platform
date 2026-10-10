# Agent 配置合并

## 配置归属

- `configs/agent-settings.yml`：全局及 mode 预置、创建默认值、Workspace 规则文件、顶层 ACP bridges。
- `configs/agent-prompt.yml`：`shared`（runtime/skill/reference/tool-appendix/plan-execute/btw/planning-mode）、`coder`、`kbase` 提示词。注入时机不变。`shared.skill.instructions-prompt`（技能适用性判断与 SKILL.md 读取规则的唯一来源）、`shared.reference.protocol-prompt`（`[References]` 引用协议）和 `shared.reference.advanced-protocol-prompt`（开启 advanced-user-prompt 时在前者之上追加的 XML 包装协议）没有源码兜底：缺失或为空时对应段落不追加，分发正文见 `configs/agent-prompt.example.yml`。规划、BTW、PLAN-EXECUTE 的指令文本同样只来自本文件：缺失时规划与 BTW 不带指令，PLAN-EXECUTE 模板只保留任务与请求数据；技能目录、披露和工具附录标题仅保留极简标签作为兜底。KBASE 提示词由 `kbase.capability-prompt`（kbase_* 工具的检索规则，挂了知识库的普通 Agent 也使用）、`kbase.system-prompt`、`kbase.workspace-prompt` 和仅在 editing 时追加的 `kbase.editing-prompt` 按此顺序拼接，四项都没有源码兜底。
- `configs/tools.yml`：访问策略、Bash/FileTools/run-env，以及顶层 vision-recognize/web-fetch/image-generate。AI profile 的 system-prompt 随 profile 保存。
- `configs/runtime.yml`：平台运行设置、`kbx.embedding`、`memory`。memx 继续由 memory.worker 配置，不新增 memx 节。

所有上述 YAML 在启动时读取，修改需要重启；注册表连接快照可在调用前重新同步。

## Agent 设置

agent-settings 顶层 preset-tools/preset-connectors 与 general/coder/kbase 各自的同名数组相加，按名称去重并保留首次出现顺序。mode 省略或 [] 仅表示无增量，不清除全局项。预置仍只作用于 native GENERAL/CODER/KBASE，不写入 Agent YAML。创建、编辑、展示、装配和连接器删除保护使用相同有效预置；已有显式声明不被删除。

分发示例通过 `coder.preset-tools` 为普通 CODER 提供 `regex`，CODER mode profile 的缺省工具列表不再内置该工具。

### planning-mode 工具排除

agent-settings 顶层 `planning-mode` 对原生 GENERAL、CODER、KBASE 统一生效，决定 `planningMode` 产生的两种 Run 相对 Agent 有效工具少了什么，两个列表都是排除清单：

- `exclude-tools`：规划 Run 不可用的工具。规划 Run 的工具 = Agent 有效工具 − 该列表，再由 Platform 追加 `finalize_planning`。
- `execute-exclude-tools`：由已确认计划启动的执行 Run 不可用的工具，缺省为 `ask_user_question`、`ask_user_form`。执行 Run 的工具 = Agent 有效工具 − 该列表，`finalize_planning` 由 Platform 去掉。

独立表单工具 `ask_user_form` 默认不在 preset-tools，需 Agent 显式声明；`planning-mode.exclude-tools` 默认也排除它，规划交互使用已挂载的 `ask_user_question`。两个列表显式配置时按既有覆盖规则生效，升级部署应同步检查实际配置。

约束：

- 排除按工具名匹配，也匹配同一工具定义的 key；只能去掉工具，不能给 Agent 增加它没有的工具。
- 未列出的工具一律保留，包括之后新挂载的 MCP 与连接器工具；挂载会修改数据的工具时需要同步加入 `exclude-tools`，规划只读由该配置维护，Platform 不再内置只读清单。
- 省略某个列表时使用代码内置缺省值（见 `configs/agent-settings.example.yml`），显式 `[]` 表示不排除任何工具。
- `finalize_planning` 由 Platform 管理，写入任一列表会使配置加载失败。
- 原生 GENERAL、CODER、KBASE 的 `stageSettings.planning.toolConfig.tools` 与 `stageSettings.execute.toolConfig.tools` 都不支持，出现即加载失败，统一改用本节的排除配置。
- 原生 GENERAL、CODER、KBASE 的 `stageSettings.planning` 与 `stageSettings.execute` 都不允许声明 `modelKey`（`modelConfig.modelKey` 与平铺写法都拒绝）：规划 Run、已确认计划的执行 Run 与普通 Run 一律使用 Agent 自身的 `modelConfig.modelKey`。阶段内 reasoning、sampling 等参数不受影响。
- 规划提示词：`agent-prompt.yml` 的 `shared.planning-mode.planning-prompt` 供所有原生 mode 使用，`coder.planning-prompt` 对 CODER 优先；两者都省略时不追加规划指令（源码不再内置规划提示词）。
- 普通 Run 不受该配置影响。

`<mode>.default-agent.modelKey/reasoningEffort` 只在创建时补全，已有 Agent 不受动态覆盖。GENERAL/CODER 的 budget 语义不变。kbase 节表示 KBASE Agent，不是知识库引擎。

GENERAL/CODER 的 `workspace-agents` 仅接受非空 `file`：声明即自动读取，省略整个节点即不读取；enabled 已删除。不自动读取不限制工具按权限主动读文件。GENERAL 无具体 Workspace 或使用 @root 时不读取；CODER 显式 projectConfig.promptFiles 仍优先。示例仅为 CODER 声明 AGENTS.md。

`acp-bridges` 位于顶层，配置类型独立于 CODER。Desktop 注册仅修改目标 bridge，保留其他节点、环境变量表达式及插件归属；仍需重启激活。现有 ACP mode 准入未扩展。

## KBX 模型与维护配置

```yaml
kbx:
  embedding:
    model-key:
    prompt: raw # raw 或 qwen3
```

Platform 从 runtime.yml 读取默认模型选择，并允许 library.yml 的 models.embedding 覆盖，连接仍解析共享模型注册表。中心与 Agent capability 共用 ModelConfigSource；启动时校验默认配置并保存 `<AP_RUNTIME_STATE_DIR>/kbx/index.yml`，实际调用使用同目录 libraries 下的库专属私有配置快照，通过显式 --config 选择。快照携带库级切块和编码，执行后删除；普通工具不能读取受管 StateDir。

默认 model-key 为空且库未覆盖时关闭向量配置，不回退聊天模型；非空无效模型明确失败，纯全文读取不解析模型。每次调用重新解析 registry，密钥和连接参数无需写进 library.yml。有效模型合同变更由中心执行全库 embed --force，不扫描来源或重划 chunk；重建失败/中断保持全文可读，严格向量检索在新合同完成前报未就绪。KBX 不迁移旧布局，维度不匹配明确报告。

知识库 embedding 由 library.yml 的 models.embedding 覆盖 runtime.kbx.embedding 默认，连接与密钥来自共享模型 registry；Agent YAML 不配置 embedding。Agent capability 的维护由 Platform KBX worker 调度，详见 [KBX 接入](KBX接入.md)。

## 离线迁移

先停止使用该部署的 Platform 进程。以下 config-dir 指包含 configs/ 的部署根；agents-dir 可选，传入可编辑 Agent 源目录，不是 ru-agents。

```bash
./agent-platform config-migrate --config-dir /path/to/deployment --agents-dir /path/to/runtime/agents
./agent-platform config-migrate --config-dir /path/to/deployment --agents-dir /path/to/runtime/agents --apply
```

默认只输出路径和退役项，不输出配置值。显式 apply 在全部候选校验通过后写入，备份在 `<config-dir>/config-backups/<timestamp>/`，restore.tsv 记录原文件对应关系；NEW 行表示迁移新建的文件，恢复时删除这些新文件。迁移发生写入失败时尝试恢复，任何恢复失败均报告并保留备份。

旧 general-settings/coder-settings/kbase-settings 按 mode 合并；prompts/coder-prompts/kbase-prompts 按 shared/coder/kbase 合并；tools 的 preset 移入 agent-settings，ai-tools 有效配置移入 tools。未接入的禁用 speech 示例移除，启用 speech 时要求先处理。新旧目标节点冲突直接停止，不决定覆盖顺序。

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
