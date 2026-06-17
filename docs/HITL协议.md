# HITL协议

## 当前状态

HITL 使用统一 awaiting 协议，保留 `mode` 字段，不引入 `kind`。当前等待模式为 `question`、`approval`、`form`、`plan`。

`/api/submit` 顶层固定为 `agentKey + runId + awaitingId + params`。前端不再提交 `mode`，后端按 `awaitingId` 反查当前等待态。

`/api/chats` 摘要中的 `awaiting` 来自持久化等待态；当 `awaiting.status == "awaiting"` 时，表示该 chat 当前有可恢复的等待项，`mode` 为 `question`、`approval`、`form` 或 `plan`。

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
- `plan`：来自 CODER planning 确认，`awaiting.ask.plan` 是单个对象；用户只能 `approve` 或 `reject`，reject 可带 `reason`。

整批取消统一提交 `params: []`，后端归一化为 `status:"error"` 与 `error.code:"user_dismissed"`。

## 配置与接口

- `POST /api/submit`
- `awaiting.ask`
- `request.submit`
- `awaiting.answer`

以上事件名是实时 stream / chat replay 的时间线事件。WebSocket `frame:"push"` 的摘要通知使用 `awaiting.asking` 与 `awaiting.answered`，payload 只携带等待项状态摘要；完整问题、审批项、表单和 plan 定义仍以 stream `awaiting.ask` 为准。

约束：

- `params` 顶层永远是数组。
- `params[i]` 固定对应 `awaiting.ask.questions|approvals|forms` 的第 `i` 项；`mode=plan` 固定只接受 1 项，对应单个 `awaiting.ask.plan`。
- `params` 每项允许带 `id`，但 `id` 只用于审计或日志，不用于分发。
- `awaiting.ask.timeout == 0` 表示无限等待、不自动超时；`timeout > 0` 表示按秒倒计时。
- `awaiting.answer.error.code == "timeout"` 时，`error.message` 会包含超时秒数与详细原因，并可携带 `timeoutSeconds`、`elapsedSeconds`、`reason:"submit_not_received_before_timeout"`。
- `awaiting.payload` 已删除，问题、审批项、表单定义直接内联在 `awaiting.ask`。

## 约束与注意事项

- `request.submit` 记录前端原始数组，`awaiting.answer` 才是后端归一化结果。
- `awaiting.ask` 会在发出时立即 flush 当前 JSONL step，完整现场保存在 step 的 `awaiting[]`；`CHATS.AWAITING_*` 只记录当前等待状态，不再为 `awaiting.ask` 写 event line。

## 相关文件

- `internal/hitl/`
- `internal/hitl/normalize.go`
- `internal/llm/run_stream_hitl_submit.go`
- `internal/llm/run_stream_hitl_shell.go`
- `internal/server/submit_validation.go`
- `internal/server/deferred_awaiting.go`
- `docs/手工测试用例.md`
