# AccessPolicy 与 HITL 改造

## 目标与当前边界

目标是统一工具准入、资源要求、权限决策、批准存储与执行前复检。审批本身不应扩大到未展示的文件、脚本版本、兄弟调用或业务操作。当前实现为第一阶段安全收紧和公共分析基础，尚未完成整个模块迁移；不能据此宣称 Host 任意代码已受到文件系统隔离。

## 本轮已落地

| 范围 | 行为 |
| --- | --- |
| 工具准入 | 生产 Session 冻结有效工具集，LLM 按当前阶段定义收紧，Router/Executor 再查。未挂载返回 `tool_not_mounted`。nil 且未冻结的内部执行上下文仅保留兼容接口。 |
| 路径 | `plain.txt` 按命令参数语义识别；先解析 symlink 再处理 `..`。外部新文件的批准范围是精确目标，不是最近已存在父目录。 |
| 命令效果 | 新增 `shellanalysis`。区分常见文件操作的读、写、递归、未知代码和远端 mutation；未知选项及未知程序可见路径保守检查写权限。Git/sed/awk 暂按 opaque 处理。 |
| 递归与 glob | 检查受保护子树；Host glob 限制 512 个目标、4096 个目录项；Container glob 需改为明确路径。native grep/glob 强制排除私有目录与父目录扫描中的 ChatsRoot。 |
| 私有状态 | 有效 StateDir、身份文件、Provider 凭据目录、旧连接器状态根对普通工具三档均 block；图片引用、Desktop CDP 参数文件及产物发布同样检查。Platform 自身的持久化和授权服务不受此工具策略限制。 |
| 脚本 | 自写和临时脚本不再免审。default 批准绑定调用、内容版本、cwd、环境、level；auto 自动审计，full 允许。已选中技能的可信入口机制保留。 |
| 凭据 | Host 环境改为允许名单继承，危险覆盖检查共用 `shellenv`。普通 Shell 不加载身份 Token 和挂载连接器凭据。验证的直接连接器子进程只注入本连接器凭据；带凭据的复合命令须拆分。 |
| Hook | Hook 指 `.bash-hooks` 执行前业务规则，与 `&&` 不同。连接器也匹配；解析已知 wrapper、Git 全局参数和 `.exe` 名称；合并全部 builtin confirmation。`autoApprove` 显式列出可自动批准的档位，form 始终人工。 |
| 审批 | 卡片增加系统 `policy/requirements` 和范围信息；复杂 Shell/远端 mutation 只给单次选项，提交校验禁止伪造本轮批准。保留 toolID 单次授权隔离、批准后变化拒绝、拒绝优先和 Run 终态对账。 |
| 图片审批 | vision/image 的全部参考图和 mask 合并到同一审批项，使用与实际加载一致的路径解析；单次读批准绑定 toolID，重复来源按次数消费，拒绝不读取，批准后变化拒绝。 |
| 误拦 | 引号内 Unicode 空格、已解析的 `#`、ANSI-C 字符串不再按文本黑名单误判；输入重定向转交路径检查；内联程序内容统一交给 opaque 策略。 |
| 无效配置 | 移除内部 `HITLLevel/AutoApproveLevels` 与未执行过限制的 `file-tools.max-batch-ops`。 |

## 部署迁移

1. 从真实 `configs/tools.yml` 删除 `file-tools.max-batch-ops`。保留该键会明确启动失败；Run 工具次数预算使用 `budget.tool.maxCalls`。
2. 需要自动确认的 builtin hook，在对应 subcommand 中显式声明，例如 `autoApprove: [auto_approve]`。未声明时包括 full_access 在内都需确认。不要用 `level` 数值作为 access level。
3. 普通脚本不得再依赖普通 Bash 的 `AP_ACCESS_TOKEN` 或挂载连接器的授权环境。改为单独调用已挂载的连接器 CLI。需要凭据的调用不得与其他 Shell 命令、wrapper 或重定向放在一起。
4. Agent/Skill 原有 PATH、加载器、解释器预加载、Git 执行配置等危险环境覆盖将被拒绝，需调整定义。普通 Shell 不再从宿主进程继承任意代理/业务变量；需要的普通变量应在 Agent 配置显式声明。
5. 外部文件的旧宽目录批准不再复用；自写/临时脚本在 default 会新增版本审批。无需迁移历史审批，approval/form 本来不跨进程恢复。

本地 `configs/tools.yml` 已同步删除废弃的 `max-batch-ops`，其他部署仍需执行上述迁移。默认 Bash 不读取登录 profile；管理员自定义 Shell/启动参数仍应作为受信任部署配置评审。

## 后续迁移，尚未完成

- 将现有 AccessPolicy、file write、bashsec 与业务 hook 的要求统一为完整 Requirement 模型；合并分散的批准表与编排入口，加入权限降级撤销和策略版本。
- 为每 Run 创建私有临时目录，统一 Host/Container 生命周期、清理与环境映射，替代进程共享临时根。
- 敏感读取例外：只能由可信用户控制面发起，绑定确切文件、只读、单次、限时；与普通批准完全分离。当前普通 HITL 和 full_access 均不能解除私有根保护，尚无此例外入口。
- 多 form 的顺序编排和改写后重新评估；当前多规则含 form 返回 `hitl_hook_conflict`，不能静默只执行一张表单。
- 更完整的 Git 参数/配置依赖、有限 brace 展开、子 Shell/case/函数、Windows Shell 方言一致性和 Container glob。当前不支持的形式继续保守处理，不伪装为已证明安全。
- 无人值守的审批拒绝策略、`policy explain`、完整统一审计和回归矩阵。
- 任意 Host 程序的真实隔离：现有代码仅在执行前分析与复检。获准的脚本或连接器仍以当前 OS 身份运行；硬链接、代码动态加载、子进程和校验后竞态需要隔离执行器解决。不能承诺“禁止 .state 路径”已经隔离全部凭据。

## 验证入口

反例及回归位于 `internal/accesspolicy/hardening_test.go`、`internal/tools/access_hardening_test.go`、`internal/hitl/hardening_test.go`、`internal/llm/run_stream_image_approval_test.go` 和 `internal/shellanalysis/operands_test.go`。原有 LLM combined approval、toolID 隔离、拒绝/超时/中断、运行恢复及 Server HITL 集成测试继续作为回归门槛。fixture 的 Token 和文件均为临时合成数据。

2026-09-30 在当前 macOS 工作区完成验证：

- `go test -p 4 ./...` 通过。
- `go test -race ./internal/llm ./internal/hitl -run 'Test(HostBash(Combined|BuiltinRuleChanged|ApprovalConcurrent|ApprovedSnapshot)|ImageAccess|SingleUseApproval)' -count=1` 通过。
- `git diff --check` 通过。

这些结果不代表 Windows Shell 或真实 Container Hub 的跨平台验收，也不代表操作系统隔离已经实现。
