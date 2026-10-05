# Host Bash 审批与等待排查

Host builtin 首次确认冻结 access/security/hook 要求，保留原检查优先级的主规则与 timeout。

`approve` 对全部冻结要求授予当前 toolID 的一次性许可；`approve_rule_run` 只登记公开主规则，其他要求仍仅限当前调用。多个 access 要求时主规则取首个待批准规则，不登记其他叶规则。后续调用即使复用 opaque 许可，仍须单独通过危险命令 hook。入口＋canonical cwd 不等同于删除目标目录授权。

当前批准调用在调度准备时比较完整工具参数及冻结 access 指纹，命令、cwd 或脚本内容变化不能借本轮许可覆盖。hard block 仍优先。审批摘要分别保存可选 `reviewedRuleKeys` 与 `runRuleKeys`，区分本次检查范围和实际本轮授权；旧记录仍可读取。

hook 单独需要确认时也统一经过 `hostBashApprovalNeeded`：批准后新增或改变的未授权 hook 立即以 `bash_access_approval_required`、`Executed=false` 收口，不再次进入旧 batch。已回答的 awaiting ID 不得重新进入等待；Container 与可修改命令的 form 不参与 Host builtin 合并审批。

具体协议见 [HITL协议](HITL协议.md) 和 [工具目录权限](工具目录权限.md)。

## 排查顺序

1. 按 runId、toolID 和 awaitingId 对齐 `awaiting.ask`、`awaiting.answer` 与最终 tool result，确认是否已经执行工具。
2. 检查审批主规则、`reviewedRuleKeys` 与 `runRuleKeys`，区分当前调用的一次性许可和可复用的 Run 规则。
3. 若日志出现 `duplicate awaiting.ask ignored`，检查是否错误复用了已回答的等待项，以及批准后是否新增 access/security/hook 要求；不能仅凭等待超时认定子进程卡住。
4. 核对实际运行程序与源码版本；审批要求变化应产生明确的未执行结果，由新调用重新申请审批。

回归入口：`internal/llm/run_stream_bash_combined_approval_test.go`，覆盖合并审批、规则变化与并发授权隔离。
