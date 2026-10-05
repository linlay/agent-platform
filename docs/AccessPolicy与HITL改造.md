# AccessPolicy 与 HITL 边界

## 适用范围

Host 工具通过准入、操作分析、权限决策与执行前复检约束调用：审批不扩大到未展示的文件、脚本、兄弟调用或业务操作；换用文件工具或 Bash 不能多审批，也不能绕过。Host 执行没有操作系统级文件隔离；获准执行的代码（包括本 Run 自写脚本）内部行为不受静态分析约束，文档不得称之为隔离。

## 权限规则

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
- 可执行配置：`.git` 本身、`.git/hooks/**`、`.git` 下的 `config` / `config.worktree`，按大小写归一的 canonical 路径匹配。脚本入口摘要不锁定 `package.json` scripts、`Makefile` 等完整依赖图。
- 平台私有资源：有效 StateDir（含连接器凭据和默认身份文件）与自定义身份文件。`registries/providers` 不保护；普通 `.env`、私钥不单独设限。
- editing=false 是强保证：Workspace 写入硬拒绝；工作目录或参数在 Workspace 内、效果无法分析的程序拒绝执行；Bash 默认工作目录为 `@chat`。

### 授权

只保留 once（绑定 toolID）与当前 Run 两种生命周期；不做 chat、跨 Run、持久或“始终允许”授权。Run 授权必须是明确范围（精确外部文件、精确命令与工作目录、执行内容版本），禁止按解释器或包管理器整体授权。复杂 Shell、远端修改只允许单次批准。授权不跨 Run 复用。

### 环境与连接器

环境变量按管理员可配置名单继承；动态加载、解释器预加载、Git 执行配置变量永不继承；SSH agent 只给 Git 网络操作。技能 `.runtime-env.json` 的 `PATH` 只追加额外目录到末尾，目录须在技能自身目录或 `bash.path-append-roots` 内，审批与执行使用同一份合并 PATH。连接器凭据只注入经验证的直接子进程；需要凭据的管道、重定向或包装调用须拆为独立直接命令。

## 执行边界

- 自写脚本免审仅限本 Run 经 `file_write/file_edit` 写入当前 Chat 目录、由系统解释器执行且内容未变的脚本；Workspace、公共临时目录和外来脚本不因此免审。
- 技能脚本与连接器 CLI 仍校验 canonical 入口及内容摘要；外围 Shell、业务 Hook、readonly 和 KBASE mutation gate 独立生效。
- readonly roots 和平台私有资源保护先于 writeRoots、hostAccess 与 HITL；批准不能解除硬拒绝。
- `@temp` 是启动时冻结的共享临时根，尚无每 Run 私有临时目录；任意获准 Host 代码内部的文件访问不受静态分析隔离。
- `find -exec` / `xargs` 内部执行对象、Container glob 和 Windows Shell 方言仍有分析限制；Git include 变更依赖缓存刷新，不能保证即时失效。

具体路径、脚本凭据与审批规则见 [工具目录权限](工具目录权限.md)；人工交互与恢复见 [HITL 协议](HITL协议.md)。
