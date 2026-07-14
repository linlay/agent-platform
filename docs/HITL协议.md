# HITL协议

## 当前状态

HITL 使用统一 awaiting 协议，保留 `mode` 字段，不引入 `kind`。当前等待模式为 `question`、`approval`、`form`、`planning`。

`/api/submit` 顶层固定为公开 owner + `runId + awaitingId + params`。普通 Agent/legacy Team 的 owner 是 `agentKey`；orchestrated Team 的 owner 是 `teamId`，不能提交隐藏协调器 key。前端不再提交 `mode`，后端按 `awaitingId` 反查当前等待态。

`/api/chats` 摘要、`/api/agents?includeChats=...` 的 `chats[]` 与 `/api/chat` 详情中的 `awaiting` 都来自持久化等待态；当 `awaiting.status == "awaiting"` 时，表示该 chat 当前有可恢复的等待项，`mode` 为 `question`、`approval`、`form` 或 `planning`。完整等待内容仍以 `events` 中的 `awaiting.ask` 为准。

子智能体 HITL 沿用主 run：普通 `agent_invoke` 和 TEAM 成员都不会注册独立 active run，`awaiting.ask.runId` 是主 `runId`，`taskId` 表示子任务归属。对子智能体等待项，前端看到和提交的 public `awaitingId` 形如 `taskId:rawAwaitingId`；后端 submit 时会映射回子工具实际等待的 `rawAwaitingId`。兼容旧前端把 `taskId` 放进 `/api/submit.runId` 的 payload，但推荐提交 `awaiting.ask.runId` 中的主 `runId`。

orchestrated Team 的 direct 委派沿用成员原有 HITL。fanout / `team_invoke` 同一并发波次出现多个等待项时，后端只发布一个 Team-level `mode=form`：每个外层字段 id 为 `taskId:rawAwaitingId`，并保留原 question / approval / form / planning schema。客户端用 `teamId + runId + Team awaitingId` 一次提交，后端校验外层 id 与内层 `form.params` 后拆分给对应成员；成员数超过 `maxParallel` 时可按后续波次再次合并。

## 核心流程

```text
assistant tool_calls[]
  -> awaiting.ask
  -> request.submit
  -> awaiting.answer
  -> react-tool tool.result(s)
```

三态语义：

- `question`：来自 `ask_user_question`，`params` 每项提交 `answer` 或 `answers`。
- `approval`：来自 Bash HITL 或文件工具越权路径审批，用户只能 approve / approve_rule_run / reject，不能修改命令内容。
- `form`：来自 Bash HITL html form，approve 时提交修改后的 `form`，reject 可带 `reason`。
- `planning`：wire-format 中来自 CODER 的 planning confirmation，`awaiting.ask.planning` 是单个对象；用户只能 `approve` 或 `reject`，reject 可带 `reason`。它不是 `plan_*` / plan-tasks 的执行任务计划。

native CODER planning 的 `planning approve` 有独立 run 边界：后端先在当前 planning run 中记录 `request.submit` / `awaiting.answer` / `finalize_planning` tool result，并发布当前 run 的 `run.complete`；旧 live stream 随后以 `reason:"done"` 正常结束，不再追加新 run 的 `run.start`。旧 run 完成后，服务端启动新的 execute run，并通过 WebSocket push `run.started { runId, chatId, agentKey, startedAt }` 暴露新 `runId`；`startedAt` 等于该 run 注册时捕获的 epoch milliseconds。webclient 应在旧 stream done 后 attach 新 `runId` 获取执行流。新 run 自己的 stream 首部为 execution run bootstrap `request.query`，包含标准 query 字段 `requestId` / `runId` / `chatId` / `role` / `message`，然后是新 run 的 `run.start`。`planning reject` 不启动新 run，仍留在当前 planning run 中生成下一版 planning 或结束。

同一 assistant turn 的 `tool_calls[]` 是 awaiting 原子批次：只要其中任意工具需要 `question` / `approval` / `form` 等等待态，整组工具都会暂停，确认前不执行任何 sibling tool。planning confirmation 使用 `mode:"planning"`，由 `finalize_planning` 专门产生；它永久等待，不使用 HITL timeout。`approval` 类型的 builtin 等待项可合并为一个 `awaiting.ask(mode:"approval", approvals:[...])`；不同 mode 的等待项按原始 `tool_calls[]` 顺序逐个等待。全部等待项进入终态后，后端才开始执行本组工具：approve 的工具与无需确认的 sibling 正常执行，reject / timeout 的工具生成 synthetic tool result。

整批取消统一提交 `params: []`，后端归一化为 `status:"error"` 与 `error.code:"user_dismissed"`。

## 配置与接口

- `POST /api/submit`
- `awaiting.ask`
- `request.submit`
- `awaiting.answer`

以上事件名是实时 stream / chat replay 的时间线事件。每个事件的 `timestamp` 为必填 epoch milliseconds；push `awaiting.asking.createdAt` 和 `awaiting.answered.answeredAt` 使用同一 epoch-ms 契约。WebSocket `frame:"push"` 的摘要通知使用 `awaiting.asking` 与 `awaiting.answered`，payload 只携带等待项状态摘要；完整问题、审批项、表单和 planning 定义仍以 stream `awaiting.ask` 为准。

约束：

- `params` 顶层永远是数组。
- `params[i]` 固定对应 `awaiting.ask.questions|approvals|forms` 的第 `i` 项；`mode=planning` 固定只接受 1 项，对应单个 `awaiting.ask.planning`。
- `params` 每项允许带 `id`，但 `id` 只用于审计或日志，不用于分发。
- `mode=question` 会按对应问题的类型校验答案：多选题只能提交非空 `answers` 数组，其他题型只能提交 `answer`；数量必须匹配，且沿用题型的值与候选项约束。提交无效 question 答案时接口返回 `data.accepted:false`、`data.status:"invalid"`，不会写入 answer 事件或解除等待项，客户端可修正后重新提交。
- 子智能体 HITL 的 `request.submit` 与 `awaiting.answer` 会继续回显 public `awaitingId`，并携带 `taskId`，用于前端归并到子任务面板；后端内部唤醒的仍是 raw awaiting。
- run owner 校验是互斥的：Agent-owned run 缺少/错传 `agentKey` 会失败；Team-owned run 缺少/错传 `teamId` 会失败，同时传 `agentKey` 也会失败。
- `approval.options[]` 与 `plan.options[]` 的内置动作只下发 `decision` code，按钮文案由 webclient 按当前语言本地化；`question.options[].label` 仍是用户可见答案文本与答案匹配值，`form.title/form` 仍是业务或工具内容。
- 对 `question` / `approval` / `form`，`awaiting.ask.timeout == 0` 表示无限等待、不自动超时；`timeout > 0` 表示后端从发出等待项开始按真实时间独立倒计时。planning confirmation 的 `mode:"planning"` 永远省略该字段，含义同样是永久等待；前端不得为它显示倒计时。observer / attach / detach 状态不会暂停或延长后端超时。
- `awaiting.answer.error.code == "timeout"` 时，`error.message` 会包含超时秒数与详细原因，并可携带 `timeoutSeconds`、`elapsedSeconds`、`reason:"submit_not_received_before_timeout"`。
- `awaiting.payload` 已删除，问题、审批项、表单定义直接内联在 `awaiting.ask`。

## 约束与注意事项

- `request.submit` 记录前端原始数组，`awaiting.answer` 才是后端归一化结果。
- `awaiting.ask` 会在发出时立即 flush 当前 JSONL step，完整现场保存在 step 的 `awaiting[]`；`CHATS.AWAITING_*` 只记录当前等待状态，不再为 `awaiting.ask` 写 event line。
- 有 awaiting 的 tool-call 批次，确认前不会产生 sibling `tool.result`；确认后所有结果落到同 `seq` 的 `_type:"react-tool"` continuation，且每个 `tool_call.id` 在下一次模型调用前必须恰好对应一个 `role:"tool"` result。
- interrupt 取消整个 Team 根 run；steer 写入协调器 run，不直接定向某个正在等待的成员。

## 相关文件

- `internal/hitl/`
- `internal/hitl/normalize.go`
- `internal/llm/run_stream_hitl_submit.go`
- `internal/llm/run_stream_hitl_shell.go`
- `internal/server/submit_validation.go`
- `internal/server/deferred_awaiting.go`
- `docs/手工测试用例.md`
