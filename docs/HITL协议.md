# HITL协议

## 当前状态

HITL 使用统一 awaiting 协议，保留 `mode` 字段，不引入 `kind`。当前三态为 `question`、`approval`、`form`。

`/api/submit` 顶层固定为 `agentKey + runId + awaitingId + params`。前端不再提交 `mode`，后端按 `awaitingId` 反查当前等待态。

## 核心流程

```text
tool.start
  -> tool.args*
  -> tool.end
  -> awaiting.ask
  -> request.submit
  -> awaiting.answer
  -> tool.result
```

三态语义：

- `question`：来自 `ask_user_question`，`params` 每项提交 `answer` 或 `answers`。
- `approval`：来自 Bash HITL 或文件工具越权路径审批，用户只能 approve / approve_rule_run / reject，不能修改命令内容。
- `form`：来自 Bash HITL html form，approve 时提交修改后的 `form`，reject 可带 `reason`。

整批取消统一提交 `params: []`，后端归一化为 `status:"error"` 与 `error.code:"user_dismissed"`。

## 配置与接口

- `POST /api/submit`
- `awaiting.ask`
- `request.submit`
- `awaiting.answer`

约束：

- `params` 顶层永远是数组。
- `params[i]` 固定对应 `awaiting.ask.questions|approvals|forms` 的第 `i` 项。
- `params` 每项允许带 `id`，但 `id` 只用于审计或日志，不用于分发。
- `awaiting.payload` 已删除，问题、审批项、表单定义直接内联在 `awaiting.ask`。

## 约束与注意事项

- `_ask_user_approval_` 已下线，审批流来自 Bash HITL builtin confirm 或文件工具越权审批。
- 历史 `events.jsonl` 中旧的 `cancelled/reason` 形状不再兼容，新前端应按未知旧态回退展示。
- `request.submit` 记录前端原始数组，`awaiting.answer` 才是后端归一化结果。

## 相关文件

- `internal/hitl/`
- `internal/hitlsubmit/normalize.go`
- `internal/llm/run_stream_hitl_submit.go`
- `internal/llm/run_stream_hitl_shell.go`
- `internal/server/submit_validation.go`
- `internal/server/deferred_awaiting.go`
- `docs/手工测试用例.md`
