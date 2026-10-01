# AccessPolicy 与 HITL 改造

## 目标

Host 直跑是主推方案。目标是统一工具准入、操作分析、权限决策、批准存储与执行前复检：审批不扩大到未展示的文件、脚本、兄弟调用或业务操作；换用文件工具或 Bash 不能多审批，也不能绕过。本改造分多轮实施，不引入操作系统隔离；获准执行的代码（包括本 Run 自写脚本）内部行为不受静态分析约束，文档不得称之为隔离。

## 确认的方案

### 配置维度

| 维度 | 职责 |
| --- | --- |
| `mode` | GENERAL / CODER / KBASE：提示词、工具与默认值；共用 native ReAct，权限引擎不按 mode 分支 |
| `engine` | native / ACP |
| `editing` | 是否允许修改 Workspace。GENERAL、CODER 默认 true，KBASE 默认 false |
| 阶段约束 | planning、旁聊等限制当前阶段能力 |
| `accessLevel` | default / auto_approve / full_access，决定其他操作如何审批 |

Workspace 由 Agent `workspaceRoot` 决定；full_access 只把访问范围扩到 `@root` 并放行审批，不改变 Workspace。

### 权限矩阵

工具准入、阶段约束、editing、平台硬规则和显式业务规则始终优先。

| 操作 | default | auto_approve | full_access |
| --- | --- | --- | --- |
| 授权范围内读取 | 放行 | 放行 | 放行 |
| Workspace 普通修改（editing=true） | 放行 | 放行 | 放行 |
| Workspace 任何修改（editing=false） | 拒绝 | 拒绝 | 拒绝 |
| Chat 目录读写 | 放行 | 放行 | 放行 |
| 越界读取 | 询问 | 自动批准并审计 | 放行 |
| 越界写入 | 询问 | 询问 | 放行 |
| 破坏性本地操作 | 询问 | 询问 | 放行（可配置询问） |
| 写入可执行配置 | 询问 | 询问 | 放行（可配置询问） |
| 远端修改、发布、上传 | 询问 | 询问 | 放行（可配置询问） |
| 未授权脚本、未知程序 | 询问 | 自动批准并审计 | 放行 |
| 本 Run 写入 Chat 目录的自写脚本 | 免审 | 免审 | 免审 |
| 已配置技能脚本、已挂载连接器 CLI | 放行 | 放行 | 放行 |
| 满足只读条件的 Git 命令 | 放行 | 放行 | 放行 |
| 平台私有资源 | 拒绝 | 拒绝 | 拒绝 |
| 业务 Hook | 按规则 | 按 `autoApprove` | 按 `autoApprove` |

- 破坏性：递归删除，以及 `reset --hard`、`clean -f`、`checkout/restore <路径>`、`stash drop/clear`、`branch -D` 等丢弃改动的 Git 命令；单文件 `rm/mv`、覆盖和 `>` 重定向是普通修改。
- 可执行配置：`.git` 本身、`.git/hooks/**`、`.git` 下的 `config` / `config.worktree`，按大小写归一的 canonical 路径匹配。`package.json` scripts、`Makefile` 等依赖由 Run 授权绑定依赖摘要处理（后续轮次）。
- 平台私有资源：有效 StateDir（含连接器凭据和默认身份文件）与自定义身份文件。`registries/providers` 不保护；普通 `.env`、私钥不单独设限。
- editing=false 是强保证：Workspace 写入硬拒绝；工作目录或参数在 Workspace 内、效果无法分析的程序拒绝执行；Bash 默认工作目录为 `@chat`。

### 授权

只保留 once（绑定 toolID）与当前 Run 两种生命周期；不做 chat、跨 Run、持久或“始终允许”授权。Run 授权必须是明确范围（精确外部文件、精确命令与工作目录、执行内容版本），禁止按解释器或包管理器整体授权。复杂 Shell、远端修改只允许单次批准。Run 等于一次用户消息，多轮开发中同一操作每轮会重新询问，以审批次数统计观察。

### 环境与连接器

环境变量按管理员可配置名单继承；动态加载、解释器预加载、Git 执行配置变量永不继承；SSH agent 只给 Git 网络操作。技能 `.runtime-env.json` 的 `PATH` 只追加额外目录到末尾，目录须在技能自身目录或 `bash.path-append-roots` 内，审批与执行使用同一份合并 PATH。连接器凭据只注入经验证的直接子进程；受管执行图（管道、重定向、`cd`）属于后续轮次。

## 第 1 轮（已提交 f4b438eb）

工具准入冻结、物理路径解析、外部目标精确批准范围、递归与 glob 受保护子树、平台私有目录硬拒绝、Host 环境允许名单与连接器直接子进程凭据、业务 Hook 包装识别与 `autoApprove`、复杂/远端单次批准、图片合并审批、移除 `HITLLevel` 与 `max-batch-ops`。

## 第 2 轮（本轮）

| 范围 | 行为 |
| --- | --- |
| 命令语义 | `internal/shellanalysis` 建立命令说明表：长短选项与参数个数、操作数角色、递归、删除、远端修改。未知选项为“分析不完整”（可见文件升级为写），不再视为执行代码。新增 `jq`、`tar`、`diff`、`rmdir` 语义；sed/awk 采用可验证安全子集；`bash -c`、`sh -c` 字面量脚本与 `env -S` 递归分析，Hook 也能匹配其中命令。 |
| Git | 只读子命令在生效配置无 fsmonitor、外部 diff、textconv、filter、签名显示时直接放行；本地写入另要求无 hook、编辑器、签名程序；网络操作另要求标准凭据助手。配置通过系统 `git config --list` 扫描，按配置文件元数据缓存。 |
| 破坏性与远端 | 新增 `approvals.destructive`、`executable-config`、`remote-mutation`，旧配置按同档位内置默认补齐，`inherit` 档位继承父档位。 |
| editing | `QuerySession.WorkspaceReadOnly` 与 KBASE `ScopedFilePolicy` 统一为 editing 能力；Workspace 写入不再由 write-roots 决定；强保证与 `@chat` 默认工作目录。KBASE 保留 `kbase_editing_mode_required` 错误码，管理员 readonly 优先显示。 |
| 自写脚本 | 本 Run 经 `file_write/file_edit` 写入当前 Chat 目录、系统解释器执行且内容未变的脚本免执行审批；公共临时目录、Workspace 与外来脚本不免审。 |
| bashsec | 只保留 AST 无法表达的硬拒绝（控制字符、引号外 Unicode 空白、危险内建、IFS、`/proc/*/environ`、zsh 模块）与无法分析时的审批；删除旧字符串校验器与内嵌脚本黑名单。多行命令、`$'..'`、URL `#`、`< file`、`{a,b}`（静态有界展开）不再误拦；输出重定向只作为文件写入判定。 |
| 路径 | Windows junction/reparse point 按链接解析；未配置的 `@agent/@skills` 不再回退为 Workspace 子目录；不透明程序参数只把 `.`、`..` 和含分隔符/`~` 的词当路径。 |
| 环境 | `bash.inherit-env`（默认含代理、证书、工具链、`XDG_*`）、GUI 启动 PATH 补齐（macOS `/etc/paths*` 与 Homebrew 前缀）、技能 PATH 追加与 `bash.path-append-roots`、SSH agent 按需注入。 |
| 私有目录 | 保护范围收敛为 StateDir 与身份文件；从访问控制和 credentialview 中移除旧连接器状态目录（启动迁移代码保留）。 |
| 配置 | 删除 `sandbox-bash` bashsec 覆盖（保留该节只记录警告）；默认 write-roots 不再含 `@workspace`。 |
| 统计 | 每个 Run 结束时记录 `[approval] asked/autoApproved`。 |

## 部署迁移

1. `configs/tools.yml` 中 `sandbox-bash` 节可删除；保留时启动记录警告，无行为影响。
2. 需要 full_access 继续确认破坏性或远端操作时，在 `full_access.approvals` 设置 `destructive: hitl`、`remote-mutation: hitl`。
3. 普通 Shell 不再继承未列入 `bash.inherit-env` 的宿主变量；需要额外变量时加入该名单或在 Agent 配置声明。
4. 技能 `.runtime-env.json` 中抄入的完整系统 PATH 会被去重，仅保留技能目录或管理员名单中的额外目录。
5. 依赖普通 Shell 中 `AP_ACCESS_TOKEN` 或连接器凭据的脚本需改为单独调用已挂载的连接器 CLI（第 1 轮已生效）。

## 后续轮次

- 第 3 轮：统一 Action 模型、Evaluate、GrantStore（替代分散批准表）、单一审批编排与执行许可；editing 作为能力输入接入统一评估。
- 第 4 轮：对接 GENERAL/native 合并与 mode/editing 配置迁移；审批卡片、Run 授权展示与撤销、项目任务授权绑定依赖摘要。
- 第 5 轮：连接器受管执行图（管道、重定向、`cd`）、全工具效果评估（产物外发、web_fetch 解析地址校验、Desktop）、每 Run 私有临时目录（接入后纳入自写豁免）、无人值守立即拒绝、ACP 权限请求桥接（`allow_always` 由平台按 Run 记忆并仅回 `allow_once`）。
- 第 6 轮：`policy explain`、审计视图、黄金回归矩阵进入 CI、移除旧链路。
- 尚未覆盖：`find -exec` / `xargs` 的内部执行对象分析、Git include 文件变更的即时失效（当前依赖 30 秒缓存）、Container glob 展开、Windows Shell 方言一致性。

## 验证入口

`internal/shellanalysis/operands_test.go`、`internal/accesspolicy/capability_test.go`、`internal/accesspolicy/hardening_test.go`、`internal/bashsec/bash_security_test.go`、`internal/hitl/hardening_test.go`、`internal/tools/command_env_test.go`、`internal/runtime/session/skill_path_test.go`、`internal/catalog/skill_runtime_env_test.go`。原有 LLM combined approval、toolID 隔离、拒绝/超时/中断、运行恢复及 Server HITL 集成测试继续作为回归门槛。依赖本机 `rg` 的少数工具测试在缺少 rg 时会失败，可设置 `AP_BUILTINS_BIN` 指向本地 builtin 缓存。
