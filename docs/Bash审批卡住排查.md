# Host Bash 批准后隐藏等待（2026-09-30）

## 结论

本次 `rmdir` 在执行前有两层独立要求：AccessPolicy opaque 审批与 `platform-admin` 技能的 dangerous-commands hook。旧 Host Bash 预检先返回 opaque 审批，用户批准后才走到 hook 分支；该分支直接返回新 request，没有调用已有的 `hostBashApprovalNeeded` 变化保护。调度器在同一模型 step 再建 batch，复用已回答的 awaiting ID，stream 正确去重了第二个 ask，后端却开始第二次等待。因此 Desktop 无新提示，600 秒后产生超时；不是删除子进程卡住。

## 现场事实

时间均为 Asia/Shanghai。只读检查日志、配置、进程及包信息，没有执行现场删除命令、修改 runtime 或重启服务。

- Chat：`/Users/linlay/.cutej/chats/6dcc6ba6-e0a9-4593-ab44-be6e23261569.jsonl`，Run：`munlxbtm`，tool ID：`call_LoGMMPmtFEoKoMU1tfZCgOgz`。
- 第 40 行，12:31:56.008：`await_batch_munlxbtm_5`，规则 `bash-access:opaque:5a24a25b4195f193`，timeout 600，命令为清理 Claude 两个安装空目录的 `rmdir`，cwd 为 `@chat`。
- 第 41 行，12:31:58.363：`approve_rule_run` 已接受并持久化为 `awaiting.answer(status=answered)`，durationMs 2355。
- 服务日志 `~/.cutej/.desktop/logs/services/agent-platform/agent-platform.log` 第 10522 行，同秒：`duplicate awaiting.ask ignored awaitingId=await_batch_munlxbtm_5`。
- 第 42 行，12:41:58.378：同 ID 的 timeout answer，恰为批准后约 600 秒。
- 第 43 行：tool result 是 `hitl_timeout`，durationMs 602383；审批审计的规则已变成 **`dangerous-commands::rmdir::::1::builtin::confirm_dialog`**，decision 为 reject，reason 为 timeout。该审计直接证明超时的是第二层 hook。
- 现场 `ru-agents/cutej/skills/platform-admin/.bash-hooks/dangerous-commands.yml` 明确包含 `rmdir`、空 match、level 1，也包含独立的 `rm` 规则。
- opaque key 可独立重算：`SHA256("/bin/rmdir" + NUL + "/Users/linlay/.cutej/chats/6dcc6ba6-e0a9-4593-ab44-be6e23261569")[:8]` 的十六进制恰为 `5a24a25b4195f193`。源码 `ordinaryCommands` 包含 `rm` 而不包含 `rmdir`，后者进入 opaque 分支。前一个 `rm` 的成功不能证明这一双层路径无问题。

## 运行版本

- Desktop main.log 的安装/启动记录、init-state 和 manifest 都指向 v0.4.20。
- 只读进程检查：PID 86609，启动于 2026-09-29 23:02:10，执行文件为 `/Users/linlay/Library/Application Support/CuteJ/services/agent-platform/v0.4.20/backend/agent-platform`。
- `go version -m`：Go 1.26.1，darwin/arm64，CGO_ENABLED=0，`vcs.revision=c39fe3267c1930b5e40beaeccfd977914a4d9485`，`vcs.modified=true`。
- 二进制 SHA-256：`397d760ea62eb2983bf95590bb476006e097f108e367686dbf2e8d7e905d21c8`。
- 本次修复前工作树 HEAD 为 `f7adf21c`；从 `c39fe326` 到该 HEAD，`run_stream_bash_authorization.go` 与 `run_stream_hitl_batch.go` 无差异。现场二进制也包含 `Bash requirements changed after approval; retry for a new review.` 字符串。

因此不能说现场只是没有升级到“要求变化保护”。已有保护漏掉了 hook 返回分支，当前源码仍可复现。由于发布构建标记 dirty，以上不等于证明二进制全部字节对应干净 commit；没有恢复发布时全部未提交修改，也没有对运行进程做调试注入。根因判断依据是持久化的两种规则、精确时间链、规则哈希与当前同路径确定性复现，而非假设整包源码完全一致。

## 代码因果链

1. `internal/accesspolicy/bash_execution.go` / `bash_review.go` 将非 ordinary 的 `/bin/rmdir` 识别为 opaque，`bash.go:opaqueBashPlan` 按入口与 canonical cwd 生成规则。
2. `internal/llm/run_stream_bash_authorization.go:prepareHostBashAuthorization` 先审 security/access，再审 `checkBashHITL`。旧版 access 不通过时提前返回，未把 hook 纳入首次快照。
3. `run_stream_hitl_batch.go:prepareQueuedBashApprovalBatch` 保存 shownApproval、matches 和 timeout，ID 仅由 `runId + step` 构造。
4. `awaitHITLApprovalBatchAndContinue` 消费提交、发布 answer、写入 approvalDecision；`applyHITLDecision` 为 approve_rule_run 登记首个规则。
5. 下一次准备消费 shownApproval，`grantDisplayedBashAccess` 登记冻结的 access 规则，清空待消费决策。复检 access 已通过；此时命中此前未授权的 `dangerous-commands::rmdir...`。这是不同规则，opaque 的本轮批准不授权它。命令尚未 dispatch，Executor 没有消费或执行删除授权。
6. 旧 hook 分支直接返回 request，绕过变化保护。调度器重新排队同 step、同 ID 的 batch。`internal/stream/dispatcher_await_format.go:newAwaitAskEvent` 发现 emittedAwaitings 已存在并丢弃 ask。
7. `RunControl.ExpectSubmit` 不复活已 resolved 的 ID；`awaitSubmit` 仍可创建 waiter，原提交已被消费，不会再次投递；新提交也会按 resolved 记录幂等返回或拒绝。`run_stream_hitl_flow.go:awaitHITLSubmitOrAccessLevelChange` 从第二次等待重新计时。
8. 600 秒后 `timeoutHITLBatch` 用第二层 match 生成 timeout、拒绝审计和未执行的工具结果，与第 42/43 行一致。

## 修复与授权边界

Host builtin 首次确认冻结 access/security/hook 要求，保留原检查优先级的主规则与 timeout。初版修复曾将 hook 提升为主规则并为全部要求登记本轮许可；授权风险复查后已撤掉此行为。

最终 `approve` 对全部冻结要求授予当前 toolID 的一次性许可；`approve_rule_run` 只登记公开主规则，其他要求仍仅限当前调用。多个 access 要求时主规则取首个待批准规则，不登记其他叶规则。后续调用即使复用 opaque 许可，仍须单独通过危险命令 hook。入口＋canonical cwd 不等同于删除目标目录授权。

当前批准调用在调度准备时比较完整工具参数及冻结 access 指纹，命令、cwd 或脚本内容变化不能借本轮许可覆盖。hard block 仍优先。审批摘要分别保存可选 `reviewedRuleKeys` 与 `runRuleKeys`，区分本次检查范围和实际本轮授权；旧记录仍可读取。

hook 单独需要确认时也统一经过 `hostBashApprovalNeeded`：批准后新增或改变的未授权 hook 立即以 `bash_access_approval_required`、`Executed=false` 收口，不再次进入旧 batch。没有删除事件去重、重放旧提交、泛化放行删除命令或仅给第二个 ask 换 ID。Container 与可修改命令的 form 不进入本次合并。

协议说明同步在 [HITL协议](HITL协议.md) 和 [工具目录权限](工具目录权限.md)。README 现有专题入口及 .gitignore 无需改动。

## 验证

新测试 `internal/llm/run_stream_bash_combined_approval_test.go` 使用临时目录、真实 hook 解析器和不启动 shell 的执行替身。

- 修复前：opaque/security × approve/approve_rule_run 四个子用例均失败，第二个 batch 的 ID 不变，规则变为 dangerous-commands::rmdir；reject 用例通过。
- 修复后：一次确认、一次结果；冻结 access/security/hook、保留原主规则与 timeout；本轮只复用公开主规则；并发显式拒绝隔离；审批后新 hook/变化 hook 不执行且不重排；Container/form 边界均通过。
- 既有 Host Bash 测试覆盖脚本内容变化、hard block、预算、取消、控制屏障和授权消费隔离。
- 完整关联包测试通过：`go test ./internal/llm ./internal/accesspolicy ./internal/bashsec ./internal/hitl ./internal/tools ./internal/stream ./internal/contracts`。
- 并发 race 检查通过：`go test -race ./internal/llm -run 'TestHostBash(Combined|BuiltinRuleChanged|ApprovalConcurrent|ApprovedSnapshot)' -count=1`，覆盖当前调用命令/cwd/脚本变化、后续不同目标、不同 cwd、本轮规则审计、动态提升权限不能代答 secondary hook（单项与批次）。
- 修复仅在工作树完成，未构建替换安装包，未重启现场服务；Windows 未实际运行测试。
