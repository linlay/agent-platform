# Agent 运行时组装

## 定位

Agent Platform 将可编辑事实源与执行目录分离：

```text
<AP_RUNTIME_DIR>/
├── agents/                         # Agent 定义与 Agent 自有 Skill
├── connectors-center/              # 导入、下载的外部连接器原包
├── .state/                         # Platform 通用持久化运行状态
│   └── connectors/<id>/            # 连接器授权与受管 CLI 状态
├── skills-center/                  # 共享 Skill；.package/ 保存技能包控制状态
└── ru-agents/                      # Platform 生成，禁止人工编辑
    ├── .staging/
    └── <agentKey>/
        ├── agent.yml
        ├── SOUL.md
        ├── AGENTS.md
        ├── connectors/              # 仅本 Agent 已挂载的完整连接器
        │   └── <connectorId>/       # 清单、bin/libs、skills 与引用资源
        ├── skills/                  # 普通 Agent/技能中心技能
        └── .config/
```

`agents/` 和 `skills-center/` 默认只在 Catalog 管理、编辑和组装阶段读取。Agent 配置内声明的 Skill 以及 Query、Workspace Terminal 和常规 Skill runtime 统一使用 `ru-agents/<agentKey>`。普通技能的共享目录 run-scoped 例外是 query 的 `mustUseSkills` 选中了 Agent 未配置的技能中心 Skill：该 run 只读访问该选中 Skill 的 canonical 目录，不修改稳定 `ru-agents`，也不创建或复制到额外的 run-runtime。`AgentConfigDir`、Admin Source、Agent CRUD 和“打开配置目录”仍指向原始 `agents/`。

连接器通过 `connectorConfig.connectors` 挂载后，自动导入技能元数据；完整包复制到 `ru-agents/<agentKey>/connectors/<id>`，技能正文、资源、runtime env 和 hooks 读取该包的 skills，不再重复复制到同级 skills 目录。`skillId` 保留包内原始技能名，不加前缀；同一 Agent 下与已配置技能或其他连接器技能重名时返回冲突诊断。逻辑指令路径为 `@connectors/<id>/skills/<name>/SKILL.md`；`.config` 默认值仍按 Agent 独立合并。连接器 bin 从当前 Agent 的运行包加入 PATH，Container 只读挂载所选包。连接器技能不能作为 mustUseSkills，详见 [连接器](连接器.md)。

Market 技能包不会作为一个可执行 Skill 目录存在。Platform 将每个子技能平铺到 `skills-center/<skill-id>/`，只在 `skills-center/.package/<package-id>.json` 保存包版本、归档摘要和子技能归属，用于整包更新、卸载与回滚。隐藏 `.package`、安装 staging 和 backup 都不进入 Skill Catalog；技能包 ZIP 仅作为临时请求输入，不在 `skills-center` 持久化。

`ru-agents` 不是来源追踪系统：不生成版本目录、Skill lock、provenance 或来源 API，也不进入 release bundle、`zenmind-env/package.sh` 产物或环境 overlay。服务启动或 Catalog 热重载时可从事实源完整重建。

模型的 system runtime context 同样保持这条边界：`agents_dir` 指向可编辑事实源 `agents/`，`ru_agents_dir` 指向 Platform 生成且禁止人工编辑的 `ru-agents/`，`agent_dir` 指向当前 Agent 的 `ru-agents/<agentKey>` 运行目录，`connectors_dir` 指向其 connectors 子目录。Host/local 普通运行可同时看到源目录与生成目录；Container Hub 不默认暴露 Agent 事实源，已有 `platform: agents` 挂载仍只提供生成目录 `/agents`，并在上下文中标记为 `ru_agents_dir`。沙箱治理任务缺少 `agents_dir` 时必须停止，不能从 `/agents`、`runtime_home` 或相邻目录推断事实源。

## 路径配置

`ru-agents` 固定使用 `<AP_RUNTIME_DIR>/ru-agents`，不提供单独环境变量或 YAML 覆盖。其位置随 `AP_RUNTIME_DIR` 一起调整。

该路径会解析为绝对路径，并且不能是文件系统根，也不能与 agents、skills-center、connectors-center、通用 state 根、teams、chats、memory、kbase、registries、tools、owner、root、automations 或 pan 等目录相同或互相包含。

## Skill 来源选择

`skillConfig.skills` 仍只声明 Skill ID，不增加 `source`：

1. `<agentsDir>/<agentKey>/skills/<id>` 存在时，必须是带合法 `SKILL.md` 的 Agent 自有 Skill。
2. 本地目录存在但不合法时，Agent 无效，不回退技能中心。
3. 本地不存在时，从 `<skills-center>/<id>` 读取。
4. 两处都不存在或技能中心 Skill 非法时，Agent 无效。
5. 重复 ID 保留第一次。

普通 Agent 自有或技能中心的选中 Skill 会完整复制到 `ru-agents/<agentKey>/skills/<id>`，包括 `SKILL.md`、`.bash-hooks`、`.runtime-env.json`、scripts、references 和 assets。Standalone YAML Agent 会在运行目录生成规范的 `agent.yml`，只能使用技能中心 Skill。

目录型 Agent 的专属 Skill 可由 Admin Agent 页面导入。导入时 Key 自动取 ZIP 内 `SKILL.md` frontmatter 的 `key`，没有 `key` 则取 `name`；ZIP 只写入 `<agentsDir>/<agentKey>/skills/<id>`，并自动将 ID 加到 `skillConfig.skills`，它永远不复制到 `skills-center`。若与技能中心 Skill 同名，该 Agent 使用专属版本，其他 Agent 继续使用技能中心版本。专属 Skill 的删除仅能在该 Agent 的管理入口完成；技能中心不会展示、编辑或删除它。

## 单次 run 的 `mustUseSkills`

`POST /api/query` 可以用 `mustUseSkills: []` 强制本次 run 使用一个或多个 Skill。这不会修改 Agent YAML，也不会改变 `ru-agents/<agentKey>`：

- 已在 `skillConfig.skills` 中配置的 key 从稳定运行目录解析，指令路径是 `@skills/<key>/SKILL.md`。
- 未配置的 key 必须属于当前有效 skills-center catalog，并在 run 启动和 continuation 时重新验证真实 `SKILL.md`；指令路径是 `@skills-center/<key>/SKILL.md`。
- 只要有一个 key 不可用，整个 run 以 `must_use_skill_unavailable` 失败，不执行其余部分。
- Prompt 按请求顺序列出全部精确路径，并把“读取且遵循全部指令”作为强制约束。
- 每个选中的已配置或额外 Skill 都解析为最终 canonical 目录，并进入本 run 的 trusted read + readonly roots；整个选中目录免读路径 HITL，未选中的 skills-center 兄弟目录不继承，symlink 逃逸按最终目标重新判权。
- 不复制 Skill、不生成文件快照、不创建 `run-runtime/`；运行中读取技能中心当前内容。脚本执行另有本 Run 内存凭据，内容变化后不继续享有入口豁免。

额外技能中心 Skill 的目录内容、scripts、references 和 assets 仍按只读访问。run readonly 先于 writeRoots、hostAccess、`full_access` 和 HITL，不能通过 exact/rule approval 写入选中目录。本次动态选择不合并它的 `.config`、`.runtime-env.json` 或 `.bash-hooks`，也不注入 Tool、MCP、其他 mount、Agent hostAccess 或更高 `accessLevel`。这些运行时扩展只有写入 Agent `skillConfig.skills` 并完成常规 `ru-agents` 组装后才生效。

Agent YAML 已配置的普通 Skill，以及本次 `mustUseSkills` 选中的 Skill，其 `scripts/**` 入口在 Run 准入时建立独立的内存执行凭据（canonical 路径、内容 SHA-256、Agent/Run/执行环境）。匹配通用脚本入口且执行前复验通过时免入口 HITL；不改变读取策略，不授权未配置、未选中技能，也不扩大外围 Shell、写入或工具权限。凭据不落盘、不跨 Run 继承；同 Run 压缩保留，跨进程恢复不重建。详见 [工具目录权限](工具目录权限.md#技能脚本入口执行凭据)。

## `.config` 合并

每个选中 Skill 根目录的 `.config/**` 自动合入最终 Agent `.config/**`：

- Skill–Skill 同路径文件内容完全相同：允许。
- 同路径内容不同：Agent 无效。
- 文件/目录结构冲突：Agent 无效。
- Windows 大小写折叠后的路径冲突：Agent 无效。
- Agent 自己的 `.config` 最后应用，可覆盖同名文件，也可用文件或目录替换 Skill 冲突子树。

`.config` 只适合 Skill 可分发的非敏感默认值。真实凭据应放在 Agent 私有 `.config`、部署 Secret 或环境变量中。生成目录及文件使用私有权限，Platform 不通过 Catalog API 回显配置内容。

`.runtime-env.json` 不使用上述冲突规则。运行时顺序保持：

```text
Agent runtimeConfig.env
  < Skill 1 .runtime-env.json
  < Skill 2 .runtime-env.json
  < ...
  < current run dynamic env
  < invocation env
  < Platform reserved context
```

后声明 Skill 覆盖前面的同名键。动态层由 `platform_control run.env.set/unset` 修改当前普通 native root run 的进程内 Scope，不写回 Agent、Skill、`ru-agents` 或其他持久化存储；Platform 重启后的续接 run 从空动态层开始。`mustUseSkills` 不合并额外 Skill runtime env，也不会挂载 `platform_control`。`AP_AGENT_CONFIG_HOME`、`AP_WORKSPACE_DIR`、`AP_CHAT_DIR`、`AP_ACCESS_TOKEN` 都是 Platform 保留变量，Agent、Skill、动态层和调用级 env 不得声明。前三者按 Host/Container 执行上下文最后注入；Workspace Terminal 只注入前两个变量；`AP_ACCESS_TOKEN` 由普通 Agent Host Bash 和已挂载 oneid-token 连接器的 stdio MCP 在进程创建前读取有效 identity 文件后注入，默认文件为 `<有效 StateDir>/identity/access-token`，显式 `--identity-file <absolute-path>` 优先。

ExecutionContext 的同一 root run 并发 clone 共享动态 Scope；构建子任务 session 时即使复用相同 RunID 也禁止取得 root Scope。`run_query` 新 root、子 Agent、Team、Terminal、MCP、ACP、Proxy、Channel、LSP、sidecar 与长期服务都不继承。

## 发布与热重载

启动时 Platform 无条件删除并重建整个 `ru-agents/`，不会复用上次进程遗留的稳定目录或 `.staging`；随后为每个 Agent 建立候选目录，重新解析 Agent 和 Skill runtime 内容，全部校验成功后才安装到新的稳定目录。启动组装失败的 Agent 不会留下旧执行副本。

运行中的热重载不会清空整个根目录：

- 活动 Run、子 Agent 调用、Team 成员和 Terminal 持有目录租约；被占用 Agent 保留原定义及文件，最后一个使用者结束后触发重载。删除 Agent 同样延迟清理。
- 空闲且含连接器的 Agent 在 `.staging` 组装完整候选，再整目录替换；未变化时保留稳定目录。替换失败尝试恢复旧目录。Windows 对目录重命名的访问拒绝、共享冲突及锁冲突进行有界退避重试，macOS 与其他 Unix 平台立即返回错误；目标路径被其他写入方重新创建时拒绝覆盖。失败诊断保留发布阶段、操作系统错误码、进程、重试次数、路径元数据及回滚结果，不读取配置正文。回滚失败时保留旧目录备份并报告其路径。
- 无连接器的 Agent 沿用稳定根中的逐文件同步方式。
- 候选校验失败保留原执行文件；该 Agent 的新定义显示无效诊断，其他 Agent 继续发布。
- 本地 MCP 绑定与 Agent 发布共同阻止新租约准入，避免目录和路由错配；远端发现与重试仍在后台执行。

`agents/`、`skills-center/` 或 `connectors-center/` 变化触发 Agent 重组。生成的 `ru-agents/` 不加入 watcher，不再存在独立 `ru-connectors/`。已有 Run 的 prompt、env、工具和 Team snapshot 保持原快照；新 Run 读取已发布的定义。连接器认证状态始终保存在 `.state/connectors`，不随 Agent 运行目录重建。

## Sandbox 与保留变量

Container Hub：

- `/agent` -> `ru-agents/<agentKey>`，`ro`
- `/skills` -> `ru-agents/<agentKey>/skills`，`ro`
- 显式 `platform: agents` 的 `/agents` -> `ru-agents`
- 显式 `platform: skills-center` 挂共享技能中心；若本次 `mustUseSkills` 含额外技能中心 Skill，则即使 Agent 未显式声明也动态追加一次 `/skills-center` 整个技能中心只读挂载，已有同类挂载去重。整个 mount 仅表示容器可见性，AccessPolicy 免审读仍只覆盖选中 Skill 目录

Host Tool：

```text
AP_AGENT_CONFIG_HOME=<ru-agents>/<agentKey>/.config
AP_WORKSPACE_DIR=<canonical workspace>
AP_CHAT_DIR=<chatsDir>/<chatId>
```

Workspace Terminal：

```text
AP_AGENT_CONFIG_HOME=<ru-agents>/<agentKey>/.config
AP_WORKSPACE_DIR=<canonical workspace>
```

Workspace Terminal 是 Agent/Workspace 级长生命周期 PTY，不注入 `AP_CHAT_DIR`；未来若需要 Chat Terminal，必须使用独立显式类型。

普通 Agent Host Bash 还可获得：

```text
AP_ACCESS_TOKEN=<有效 identity 文件当前非空单行内容>
```

该 token 每次新建 Bash 都重新读取，不缓存；Workspace Terminal、`file_grep/file_glob`、Container、Proxy、ACP、MCP、LSP 和 sidecar 不自动获得它。文件缺失、不可读、为空或非法时省略变量，Bash 仍正常启动。

Container 中三个值分别是 `/agent/.config`、`/workspace`、`/chat`。Workspace/Chat 双根和 KBASE 的 `runtimeConfig.workspaceRoot` 契约不受 Agent 组装影响。

Host run 的额外 `mustUseSkills` 不创建 mount：session 按需暴露真实 `@skills-center` 语义根，但 trusted read + readonly roots 只注册本次选中的 canonical Skill 目录。未选择额外 Skill 且 Agent 未显式配置技能中心挂载时，该路径仍不暴露。

## 技能包事务与目录监听

Platform 普通技能创建、导入、删除与文件编辑（包括 `/api/admin/source`），以及技能包导入、整包删除和包内单技能删除，与后台 Catalog 发布共用串行保护区。ZIP 解压、安全检查和静态校验先在保护区外完成，发布前再检查当前 revision、归属与引用。技能快照和普通文本保存只进入保护区，不停止 watcher；目录发布、重命名和删除只同步释放对应资源根的监听句柄，提交或回滚后恢复。

Catalog 按资源根建立独立 watcher；配置根重叠时合并后端，避免重复监听。所有事件由统一调度器合并分类，实际 reload 仍串行执行；技能操作不会暂停其他非重叠资源根的监听。API 显式 reload 后记录加载前后均一致的内容指纹（路径、权限与文件内容，不以 mtime 代替内容），重复和迟到事件只核对差异。恢复监听触发对应类别的补偿检查，内容没有变化就不再 reload，不再无条件全量重载。加载失败或加载期间内容变化不确认该状态；其他类别和加载期间的新事件继续排队。

现有 `skills → agents` 加载链路保留，用于同步 `ru-agents` 和内存 Catalog；本次未实现按单个 Agent 依赖增量组装，也不改变活动 Run 的租约保护。指纹核对只在事件/API 重载时执行，不做固定周期轮询；大型资源根会增加文件读取成本，需要按实际目录规模验证。

解压与备份使用技能中心相邻的隐藏事务目录，不进入技能扫描根；正常部署中二者位于同一卷，目录发布保持原子 rename，禁止跨卷复制降级。监听注册与事件过滤均排除事务目录及其后代，`.package` 元数据不作为普通技能内容触发重载。

回滚只逆转已成功执行的备份和发布，不删除尚未移动的原目录。回滚失败时保留旧资源备份和原始包记录，不在启动时自动删除技能中心外的恢复目录；错误必须保留恢复位置供诊断。该保护覆盖 Platform 技能管理入口；连接器导入、编辑、删除复用同一保护，仍保留原有使用中与准备中拒绝规则。Desktop 应通过管理 API 发布或条件恢复，不直接移动正式目录；智能体应在 Workspace/临时目录准备资源后调用管理入口。Bash、外部编辑器及其他进程直接写盘不受此进程内锁约束，watcher 仅提供变化发现与最终同步，不承诺多文件写入的事务隔离。本次不增加 Host Bash 文件系统隔离。
