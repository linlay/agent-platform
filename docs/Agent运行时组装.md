# Agent 运行时组装

## 定位

Agent Platform 将可编辑事实源与执行目录分离：

```text
<AP_RUNTIME_DIR>/
├── agents/                         # Agent 定义与 Agent 自有 Skill
├── connectors-center/              # 导入、下载的外部连接器原包
├── ru-connectors/<id>/<digest>/     # 跨 Agent 共享的完整运行包
├── .state/                         # Platform 通用持久化运行状态
│   └── connectors/<id>/            # 连接器授权与受管 CLI 状态
├── skills-center/                  # 共享 Skill；.package/ 保存技能包控制状态
├── ru-skills/<digest>/             # 普通 Skill 的共享只读内容快照
└── ru-agents/                      # 仅当前进程使用，禁止人工编辑
    ├── .staging/
    └── <agentKey>/<revision>/
        ├── agent.yml
        ├── SOUL.md / AGENTS.md
        ├── .revision               # 完整摘要；目录使用 24 位前缀并检查碰撞
        ├── connectors/<id>.json    # 连接器挂载引用
        ├── skills/.refs.json        # 普通 Skill ID 与完整内容摘要，不复制正文
        └── .config/                # 按 Agent 版本合并的只读静态配置
```

`agents/` 和 `skills-center/` 默认只在 Catalog 管理、编辑和组装阶段读取。Agent 配置内声明的 Skill 以及 Query、Workspace Terminal 和常规 Skill runtime 统一使用 `ru-agents/<agentKey>/<revision>`。普通技能的共享目录 run-scoped 例外是 query 的 `mustUseSkills` 选中了 Agent 未配置的技能中心 Skill：该 run 只读访问该选中 Skill 的 canonical 目录，不修改已发布 `ru-agents`，也不创建或复制到额外的 run-runtime。`AgentConfigDir`、Admin Source、Agent CRUD 和“打开配置目录”仍指向原始 `agents/`。

连接器通过 `connectorConfig.connectors` 挂载后，自动导入技能元数据；完整包按内容摘要共享安装到 `ru-connectors/<id>/<contentDigest>`，技能正文、资源、runtime env 和 hooks 读取该包的 skills，不再重复复制到同级 skills 目录。`skillId` 保留包内原始技能名，不加前缀；同一 Agent 下与已配置技能或其他连接器技能重名时返回冲突诊断。逻辑指令路径为 `@connectors/<id>/skills/<name>/SKILL.md`；`.config` 默认值仍按 Agent 独立合并。连接器 bin 从当前 Agent 的运行包加入 PATH，Container 只读挂载所选包。连接器技能不能作为 mustUseSkills，详见 [连接器](连接器.md)。

技能包保存在 `skills-center/<package-id>/`，包根 `package.json` 必须包含 `name` 与 `skills:[{"id":"pdf"}]` 成员清单。成员 id 为包内单段目录名，至少保存 id，其他扩展属性原样保留；名称、描述和版本读取成员 SKILL.md。Platform 按清单加载包下一层成员，生成精确技能 ID `<package-id>/<skill-id>`，不递归发现技能，不加载未声明目录；声明但缺失、不可读取或不安全的成员保留管理诊断，不使整包消失。顶层 `<skill-id>` 与包内同名技能可共存，安装、更新、编辑和卸载分别作用于精确 id；包更新只替换该包目录，不接管或覆盖顶层同名技能。普通技能内的 `sub-skills` 暂不扫描。技能包目录本身不可执行，也不能通过单技能接口覆盖；隐藏 staging 和 backup 不进入 Skill Catalog，临时 ZIP 不持久化。Platform 在首次启动 Catalog、开始监听目录前迁移旧 `.package` 记录：复制成员到新包目录，保留原顶层技能以兼容旧 Agent 短 id 引用，旧清单移入可恢复备份并记录路径。迁移完成后幂等；目标同名冲突保留原记录和已有目录，记录 skill_package_migration_conflict 并跳过该项，继续迁移其他包，不阻断服务启动。其他迁移错误仍保留备份并报告，不覆盖已有目录。普通查询和热重载不触发迁移。已安装 package.json 缺少 skills 时，启动阶段补齐一次显式声明，并在技能根外保留原文件备份；已有 skills 的无效清单不自动修复。旧 .package 的缺失成员声明同样保留为诊断占位。

`ru-agents` 与 `ru-skills` 是进程内中间产物，不保存历史，不进入 release bundle、环境资源包或 overlay。启动清空重建，不新增 `.state` 定义快照、Run pin 或格式迁移。跨重启等待确认的 Run 沿用现有恢复流程，重新绑定当前 Agent 与普通 Skill；连接器仍使用现有持久快照恢复。

模型的 system runtime context 同样保持这条边界：`agents_dir` 指向可编辑事实源 `agents/`，`ru_agents_dir` 指向 Platform 生成且禁止人工编辑的 `ru-agents/`，`agent_dir` 指向当前 Agent 的 `ru-agents/<agentKey>/<revision>` 运行目录，`connectors_dir` 使用逻辑入口 `@connectors`，按当前 Agent 的挂载快照解析到共享包。Host/local 普通运行可同时看到源目录与生成目录；Container Hub 不默认暴露 Agent 事实源，已有 `platform: agents` 挂载仍只提供生成目录 `/agents`，并在上下文中标记为 `ru_agents_dir`。沙箱治理任务缺少 `agents_dir` 时必须停止，不能从 `/agents`、`runtime_home` 或相邻目录推断事实源。

## 路径配置

`ru-agents` 固定使用 `<AP_RUNTIME_DIR>/ru-agents`，不提供单独环境变量或 YAML 覆盖。其位置随 `AP_RUNTIME_DIR` 一起调整。

该路径会解析为绝对路径，并且不能是文件系统根，也不能与 agents、skills-center、connectors-center、通用 state 根、teams、chats、memory、kbase、registries、tools、owner、root、automations 或 pan 等目录相同或互相包含。

## Skill 调度范围

共享调度提示按用户目标、交付形态与技能触发条件判断适用性。用户指令优先；技能的可用、读取或选中不扩大任务范围，工作流与完成标准只适用于用户要求。该边界是模型提示约束，不替代工具权限或 HITL。

## Skill 来源选择

`skillConfig.skills` 声明精确 Skill ID，不增加 `source`。独立技能使用单段 ID；包成员使用 `<package-id>/<skill-id>`，不支持按成员短名回退或多层路径：

1. 单段 ID 对应的 `<agentsDir>/<agentKey>/skills/<id>` 存在时，必须是带合法 `SKILL.md` 的 Agent 自有 Skill。两段包成员 id 只从技能中心包目录读取，Agent 自有目录不遮蔽包成员。
2. 本地目录存在但不合法时，Agent 无效，不回退技能中心。
3. 本地不存在时，从 `<skills-center>/<id>` 读取。
4. 两处都不存在或技能中心 Skill 非法时，Agent 无效。
5. 重复 ID 保留第一次。

普通技能快照组装前，组装器按大小写折叠后的完整 id 检查目录边界：同一 Agent 不能同时选择独立技能 `suite` 与包成员 `suite/demo`，因为两者会产生相互覆盖的 Container 挂载点。冲突与声明顺序无关，返回包含两个 id 的 `runtime_skill_path_conflict` 诊断；热重载失败时保留已发布运行文件。`demo` 与 `suite/demo`、同一包的多个成员仍可同时选择。连接器技能在独立共享包中执行，不参与普通技能运行目录的前缀冲突检查。

普通 Agent 自有或技能中心的已配置 Skill 按完整目录内容摘要安装到 `ru-skills/<digest>`，包括 `SKILL.md`、hooks、runtime env、scripts、references、assets 和 `.config`；同内容跨 Agent/版本复用，私有 Skill 不因此变成公开技能。`skills/.refs.json` 只保存有序 ID/摘要；同级预建空 Skill 挂载点，避免 Container 在只读父挂载下创建目录。`@skills/<id>` 通过 Run 绑定解析真实目录，Bash 使用技能目录清单中的 Host 或 Container 路径。Standalone YAML Agent 仍生成规范的 `agent.yml`，只能使用技能中心 Skill。

摘要覆盖路径、内容、结构与规范化可执行位，不包含 mtime 或可写权限位。Agent 版本目录（含根目录、自有资源、引用及 .config）和共享 Skill 安装后均去除写权限；发布及新租约复验全树摘要。损坏快照拒绝新租约并触发重载，仍有租约时禁止原地替换，释放后从当前来源重新组装；不重建历史。Skill 缓存和 CLI 状态必须写到 Chat、临时目录或独立状态目录，不能写入共享快照或静态 `.config`。这些保护不隔离同用户 Host 任意程序，不能保证校验后的任意代码访问不被外部篡改。

目录型 Agent 的专属 Skill 可由 Admin Agent 页面导入。导入时 ID 自动取 ZIP 内 `SKILL.md` frontmatter 的 `id`，兼容旧 `key`，都没有则取 `name`；ZIP 只写入 `<agentsDir>/<agentKey>/skills/<id>`，并自动将 ID 加到 `skillConfig.skills`，它永远不复制到 `skills-center`。若与技能中心 Skill 同名，该 Agent 使用专属版本，其他 Agent 继续使用技能中心版本。专属 Skill 的删除仅能在该 Agent 的管理入口完成；技能中心不会展示、编辑或删除它。

## 单次 run 的 `mustUseSkills`

`POST /api/query` 可以用 `mustUseSkills: []` 强制本次 run 使用一个或多个 Skill。这不会修改 Agent YAML，也不会改变 `ru-agents/<agentKey>/<revision>`：

- 已在 `skillConfig.skills` 中配置的 id 从该 Agent 版本的 Skill 引用解析，指令路径是 `@skills/<id>/SKILL.md`。
- 未配置的 id 必须属于当前有效 skills-center catalog，并在 run 启动和 continuation 时重新验证真实 `SKILL.md`；指令路径是 `@skills-center/<id>/SKILL.md`。
- 只要有一个 id 不可用，整个 run 以 `must_use_skill_unavailable` 失败，不执行其余部分。
- 技能的 `name` 仍为子目录短名，元数据诊断按 id 的最后一段比较；运行引用和权限目录始终使用完整 id。
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

后声明 Skill 覆盖前面的同名键。动态层由 `run_env` 的 `set/unset/update` 修改当前普通 native root run 的进程内 Scope，不写回 Agent、Skill、`ru-agents` 或其他持久化存储；Platform 重启后的续接 run 从空动态层开始。`mustUseSkills` 不合并额外 Skill runtime env，也不会挂载 `run_env` 或 `platform_control`。`AP_AGENT_CONFIG_HOME`、`AP_WORKSPACE_DIR`、`AP_CHAT_DIR`、`AP_ACCESS_TOKEN` 都是 Platform 保留变量，Agent、Skill、动态层和调用级 env 不得声明。前三者按 Host/Container 执行上下文最后注入；Workspace Terminal 只注入前两个变量；普通 Host Shell 不自动获得 `AP_ACCESS_TOKEN`；经验证的单条直接 oneid-token CLI 调用在独立子进程中使用该变量，已挂载 oneid-token stdio MCP 也在进程创建前读取有效 identity 文件后注入，默认文件为 `<有效 StateDir>/identity/access-token`，显式 `--identity-file <absolute-path>` 优先。

ExecutionContext 的同一 root run 并发 clone 共享动态 Scope；构建子任务 session 时即使复用相同 RunID 也禁止取得 root Scope。`chat_start` 新 root、子 Agent、TEAM 成员、Terminal、MCP、ACP、Proxy、Channel、LSP、sidecar 与长期服务都不继承。

## 发布与热重载

启动时 Platform 清空并重建 `ru-agents/` 和 `ru-skills/`。版本仅在当前进程中并存；不影响持久的 `ru-kbases` 或现有 `ru-connectors`。

- 有效定义和候选文件共同决定 Agent revision。相同内容复用；修改配置即发布新目录，新 Run 无须等待旧 Run 结束。当前版本、活动 Run、子调用、Team、Terminal 及正在发布的资源是内存引用根。
- 租约绑定具体版本。Team 子任务续租冻结成员；执行器仍持有租约时发起的续接在继承版本准入后才释放原租约。规划 Run 结束后，通过提交接口延迟批准时重新准入，执行 Run 使用当前发布版本，期间配置修改会生效；不为等待批准持久保留旧版本。已发布定义对调用方深拷贝；最后一个旧使用者退出后回收旧目录，未被任何版本引用的普通 Skill 同时回收。
- 配置解析、校验或组装失败时，继续使用最近成功版本，管理端保留来源错误并显示 `runtime_last_good_version`。删除来源阻止新准入，旧租约继续收尾。首次启动无有效版本时仍不可用。
- 管理 Meta 提供 `runtimeRevision`、`runtimeSynchronized`、`runtimeRetainedVersions`、`runtimeLeaseCount`。这些描述进程内状态，不形成历史记录。全树损坏不允许以最近成功版本为由绕过校验。
- 发布、引用计数及 GC 使用同一进程内互斥边界。释放租约仅在至少一个版本引用计数归零时触发回收扫描；扫描与删除仍在锁内进行，重载后的回收不变。准入先在锁内冻结定义、捕获可信摘要并保留临时租约，再在锁外做全树校验；临时引用阻止 GC 或修复替换该版本，校验失败释放引用并拒绝准入。准入的版本选择以保留临时租约时为准。目录 watcher 仍串行重载，组装并发上限为 1；reload 的组装与校验仍持锁，会阻塞新的版本选择。本地绑定失败恢复原 Catalog；连接器版本化 MCP、Run pin 与回收仍使用现有机制。
- 同一进程内冻结 Agent 定义、自有文件、普通 Skill 和已冻结 TEAM 总控与成员定义；不新增模型/Provider、独立工具、全局 MCP、Memory、Owner 或知识内容的历史冻结保证。重启后等待 Run 使用当前定义继续，不因本次改造主动终结；原有安全校验和恢复错误仍生效。

`agents/`、`skills-center/`、`connectors-center/` 变化沿用现有级联。生成目录不监听、不提交、不打包。大 Skill 的全树摘要会增加准入及重载 I/O，锁外校验允许并发但不消除磁盘带宽竞争。缺失 Skill 引用元数据或冻结 Agent 路径时直接失败，不回退到旧目录布局。

准入基准：`go test ./internal/catalog -run '^$' -bench BenchmarkRuntimeAdmission -benchtime=12x`；可用 `AP_BENCH_AGENT_VERSION` 指向实际版本目录，基准先复制到测试临时目录，不修改部署。2026-10-10 在 macOS arm64 / Apple M5 上，对本机最大的 Agent（自有文件 7,716 字节、普通 Skill 3,978,476 字节）测量，每批 8 个并发、12 批：锁内校验 P95 60.18 ms，锁外 40.10 ms；额外加入 64 MiB 自有资源后，P95 从 267.2 ms 降至 71.55 ms。均为本地缓存和该文件分布下的样本，不代表冷盘或 Container Hub 性能。

## Sandbox 与保留变量

Container Hub：

- `/agent` -> `ru-agents/<agentKey>/<revision>`，`ro`
- `/skills` -> 当前版本的引用目录，`/skills/<id>` -> 对应 `ru-skills/<digest>` 的独立只读挂载；包 ID 保留两段路径
- 显式 `platform: agents` 的 `/agents` -> `ru-agents`
- 显式 `platform: skills-center` 挂共享技能中心；若本次 `mustUseSkills` 含额外技能中心 Skill，则即使 Agent 未显式声明也动态追加一次 `/skills-center` 整个技能中心只读挂载，已有同类挂载去重。整个 mount 仅表示容器可见性，AccessPolicy 免审读仍只覆盖选中 Skill 目录

Host Tool：

```text
AP_AGENT_CONFIG_HOME=<ru-agents>/<agentKey>/<revision>/.config
AP_WORKSPACE_DIR=<canonical workspace>
AP_CHAT_DIR=<chatsDir>/<chatId>
```

Workspace Terminal：

```text
AP_AGENT_CONFIG_HOME=<ru-agents>/<agentKey>/<revision>/.config
AP_WORKSPACE_DIR=<canonical workspace>
```

Workspace Terminal 是 Agent/Workspace 级长生命周期 PTY，不注入 `AP_CHAT_DIR`。

经验证的单条直接 oneid-token CLI 调用，其独立子进程可获得：

```text
AP_ACCESS_TOKEN=<有效 identity 文件当前非空单行内容>
```

该 token 在连接器子进程启动前从有效 identity 文件重新读取，不进入普通 Host Shell、Workspace Terminal、`file_grep/file_glob`、Container、Proxy、ACP、LSP 或 sidecar。身份不可用时不得复用旧 token 或继承父进程 token；连接器凭据解析错误会阻止该调用启动。oneid-token MCP 使用独立身份链路：stdio 在创建进程前注入，HTTP 按请求生成 Bearer Header。

Container 中三个值分别是 `/agent/.config`、`/workspace`、`/chat`。Workspace/Chat 双根和 KBASE 的 `runtimeConfig.workspaceRoot` 契约不受 Agent 组装影响。

Host run 的额外 `mustUseSkills` 不创建 mount：session 按需暴露真实 `@skills-center` 语义根，但 trusted read + readonly roots 只注册本次选中的 canonical Skill 目录。未选择额外 Skill 且 Agent 未显式配置技能中心挂载时，该路径仍不暴露。

## 技能包事务与目录监听

Platform 普通技能创建、导入、删除与文件编辑（包括 `/api/admin/source`），以及技能包导入、整包删除和包内单技能删除，与后台 Catalog 发布共用串行保护区。ZIP 解压、安全检查和静态校验先在保护区外完成，发布前再检查当前 revision、归属与引用。技能快照和普通文本保存只进入保护区，不停止 watcher；目录发布、重命名和删除只同步释放对应资源根的监听句柄，提交或回滚后恢复。

Catalog 按资源根建立独立 watcher；配置根重叠时合并后端，避免重复监听。所有事件由统一调度器合并分类，实际 reload 仍串行执行；技能操作不会暂停其他非重叠资源根的监听。API 显式 reload 后记录加载前后均一致的内容指纹（路径、权限与文件内容，不以 mtime 代替内容），重复和迟到事件只核对差异。恢复监听触发对应类别的补偿检查，内容没有变化则不 reload。加载失败或加载期间内容变化不确认该状态；其他类别和加载期间的新事件继续排队。

现有 `skills → agents` 加载链路保留，用于同步 `ru-agents` 和内存 Catalog；不按单个 Agent 依赖增量组装，活动 Run 受租约保护。指纹核对只在事件/API 重载时执行，不做固定周期轮询；大型资源根会增加文件读取成本，需要按实际目录规模验证。

解压与备份使用技能中心相邻的隐藏事务目录，不进入技能扫描根；正常部署中二者位于同一卷，目录发布保持原子 rename，禁止跨卷复制降级。监听注册与事件过滤均排除事务目录及其后代，`.package` 元数据不作为普通技能内容触发重载。

回滚只逆转已成功执行的备份和发布，不删除尚未移动的原目录。回滚失败时保留旧资源备份和原始包记录，不在启动时自动删除技能中心外的恢复目录；错误必须保留恢复位置供诊断。该保护覆盖 Platform 技能管理入口；连接器导入、编辑、删除复用同一保护，仍保留原有使用中与准备中拒绝规则。Desktop 应通过管理 API 发布或条件恢复，不直接移动正式目录；智能体应在 Workspace/临时目录准备资源后调用管理入口。Bash、外部编辑器及其他进程直接写盘不受此进程内锁约束，watcher 仅提供变化发现与最终同步，不承诺多文件写入的事务隔离。Host Bash 没有文件系统隔离。

目录枚举统一忽略示例目录、隐藏目录和保留的旧连接器目录；单个技能包清单损坏或名称与目录不符时，记录 invalid_skill_package 并跳过该包，不阻断其他技能和技能包。具体包的读写/安装接口仍返回明确校验错误，不静默修复内容。

### Container 版本切换验证范围

运行挂载消费 Run 冻结的 Agent 路径和 Skill 引用，不能用 Agent key 重新拼目录。挂载指纹变化会切换 Agent/Chat 容器；容器可写层中的安装或缓存不保证保留，Workspace/Chat 的持久挂载仍保留。当前测试覆盖本地挂载生成、限定技能的只读约束与嵌套 Skill ID；尚未在目标 Container Hub 验证大量挂载的上限和性能，也未完成 Windows 目标环境验证。
