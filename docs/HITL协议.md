# HITL协议

## 当前状态

HITL 使用统一 awaiting 协议，保留 `mode` 字段，不引入 `kind`。当前等待模式为 `question`、`approval`、`form`、`planning`。

`/api/submit` 顶层固定为公开 owner + `runId + awaitingId + params`。普通 Agent 的 owner 是 `agentKey`；Team 的 owner 是 `teamId`，不能提交隐藏协调器 key 或 `agentKey`。前端不再提交 `mode`，后端按 `awaitingId` 反查当前等待态。

`/api/chats` 摘要、`/api/agents?includeChats=...` 的 `chats[]` 与 `/api/chat` 详情中的 `awaiting` 都来自持久化等待态；当 `awaiting.status == "awaiting"` 时，表示该 chat 当前有可恢复的等待项，`mode` 为 `question`、`approval`、`form` 或 `planning`。完整等待内容仍以 `events` 中的 `awaiting.ask` 为准。

子智能体 HITL 沿用主 run：普通 `agent_invoke` 和 TEAM 成员都不会注册独立 active run，`awaiting.ask.runId` 是主 `runId`，`taskId` 表示子任务归属。对子智能体等待项，前端看到和提交的 public `awaitingId` 形如 `taskId:rawAwaitingId`；后端 submit 时会映射回子工具实际等待的 `rawAwaitingId`。兼容旧前端把 `taskId` 放进 `/api/submit.runId` 的 payload，但推荐提交 `awaiting.ask.runId` 中的主 `runId`。

orchestrated Team 的 `agent_delegate` 沿用成员原有 HITL。同一并发波次出现多个等待项时，后端只发布一个 Team-level `mode=form`：每个外层字段 id 为 `taskId:rawAwaitingId`，并保留原 question / approval / form / planning schema。客户端用 `teamId + runId + Team awaitingId` 一次提交，后端校验外层 id 与内层 `form.params` 后拆分给对应成员；成员数超过 `maxParallel` 时按后续波次再次合并。

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
- `platform_control` 的 `run.env.set/unset` 不使用专用 HITL；它们仍是 operation-aware barrier，并在执行时校验 key、value、limits、revision 与幂等。
- `form`：来自 Bash HITL html form，approve 时提交修改后的 `form`，reject 可带 `reason`。
- `planning`：wire-format 中来自 CODER 的 planning confirmation，`awaiting.ask.planning` 是单个对象；用户只能 `approve` 或 `reject`，reject 可带 `reason`。它不是 `plan_*` / plan-tasks 的执行任务计划。

native CODER planning 的 `planning approve` 有独立 run 边界：后端先在当前 planning run 中记录 `request.submit` / `awaiting.answer` / `finalize_planning` tool result，并发布当前 run 的 `run.complete`；旧 live stream 随后以 `reason:"done"` 正常结束，不再追加新 run 的 `run.start`。旧 run 完成后，服务端启动新的 execute run，并通过 WebSocket push `run.started { runId, chatId, agentKey, startedAt }` 暴露新 `runId`；`startedAt` 等于该 run 注册时捕获的 epoch milliseconds。webclient 应在旧 stream done 后 attach 新 `runId` 获取执行流。新 run 自己的 stream 首部为 execution run bootstrap `request.query`，包含标准 query 字段 `requestId` / `runId` / `chatId` / `role` / `message`，然后是新 run 的 `run.start`。`planning reject` 不启动新 run，仍留在当前 planning run 中生成下一版 planning 或结束。

同一 assistant turn 的 `tool_calls[]` 是 awaiting 原子批次：只要其中任意工具需要 `question` / `approval` / `form` 等等待态，整组工具都会暂停，确认前不执行任何 sibling tool。planning confirmation 使用 `mode:"planning"`，由 `finalize_planning` 专门产生；它永久等待，不使用 HITL timeout。`approval` 类型的 builtin 等待项可合并为一个 `awaiting.ask(mode:"approval", approvals:[...])`；不同 mode 的等待项按原始 `tool_calls[]` 顺序逐个等待。全部等待项进入终态后，后端才开始执行本组工具：approve 的工具与无需确认的 sibling 正常执行，reject / timeout 的工具生成 synthetic tool result。

整批取消统一提交 `params: []`，后端归一化为 `status:"error"` 与 `error.code:"user_dismissed"`。

## 跨进程重启

Platform 启动时以 `CHATS.AWAITING_*` pending summary 为入口，对照同一 awaiting 的物理 step、answer、matching tool result 与 run completion 做幂等对账。`question` 在未超时或 `timeout=0` 时恢复，`planning` 不受停机时长或历史 timeout 字段影响，始终恢复。可恢复项不只注册 deferred submit：启动 hydration 还会为原公开 `runId` 注册真实的 suspended active run，保留原 `startedAt`、公开 owner、access level 与 run scope，状态为 `WAITING_SUBMIT`，EventBus cursor 从该 run 已持久化的最大 `liveSeq` 开始。因此重启后 `/api/chat` 会同时返回权威 `awaiting` 与可 attach 的 `activeRun`，客户端应在用户点击提交之前立即使用 `activeRun.lastSeq` attach。

恢复后的 question 与 planning reject 提交会原子 claim suspended run，状态短暂进入 `RESUMING`；`request.submit` 和 `awaiting.answer` 先按连续 live seq 持久化并发布到同一 EventBus，再从原 query、raw messages、ask 与答案重建 continuation。该路径保持原公开 `runId`，不重复发布 `run.started`，已在 submit 前 attach 的连接会直接收到后续 reasoning/content/tool 事件。native CODER planning approve 则先在旧 suspended planning run 上持久化并发布 submit、answer、tool result 与 `run.complete`，关闭旧 attach 后再启动新 execution run。

`approval` / `form` 不跨进程恢复：已超时写 `error.code:"timeout"`，未超时写 `error.code:"runtime_restarted"`。run env set/unset 不使用专用 HITL。已超时的 `question` 同样写 `timeout`；无法从持久化 task/tool 上下文安全确定子智能体或 Team continuation 的 `question` / `planning` 写 `runtime_restarted`。这些自动终态不伪造 `request.submit`，而是依次持久化 `awaiting.answer(status:"error")`、同原因且 `executed:false` 的 matching tool result、`finishReason:"cancel"` 的 run completion，最后清除 pending summary。任一步写入失败都会中止 Server 初始化；再次启动会从已有记录继续，不重复写 answer、result 或 completion。

question/planning awaiting StepLine 内部保存 root Scope revision。revision 0 恢复为空 lazy scope且不要求 checkpoint；revision 大于 0 时必须从 Chat 目录外的 AES-256-GCM v2 checkpoint 解密同一 RunID 的 state，并校验 run/chat/subject/owner/agent 与精确 revision。缺失、损坏、密钥错误、身份不匹配或 v1 时以 `run_env_restore_failed` fail closed。native planning approve 创建新的 execute RunID，因此不继承旧 planning run 的动态值。

恢复后的 question 剩余 timeout 由 suspended run supervisor 继续计时；`/api/interrupt` 和 reaper 也由同一 supervisor 收口。终态会按 `awaiting.answer -> tool.result(s) -> run.cancel` 的顺序发布到已 attach 连接，然后 freeze EventBus 并移除 active run。Platform 自身关闭仅取消进程内 supervisor，不伪造用户中断，pending 留给下次 hydration。

没有 pending summary 的孤立历史 `awaiting.ask` 不迁移、也不恢复为活动态。`/api/chat.awaiting` 是客户端判断可提交状态的唯一事实源；历史 ask 只保留用于时间线和匹配完整交互内容。

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
- `/api/submit` 对已自动终态或已经由其他提交处理的 known awaiting 返回 HTTP 409：`awaiting_expired`、`awaiting_interrupted` 或 `already_resolved`。响应 `data` 携带 `chatId/runId/awaitingId/status/errorCode` 和结构化 `error`；真正不存在或 `chatId/runId/awaitingId` 身份不匹配时返回 HTTP 400 `unknown_awaiting`。
- `awaiting.payload` 已删除，问题、审批项、表单定义直接内联在 `awaiting.ask`。

## 约束与注意事项

- `request.submit` 记录前端原始数组，`awaiting.answer` 才是后端归一化结果。
- `awaiting.ask` 会在发出时立即 flush 当前 JSONL step，完整现场保存在 step 的 `awaiting[]`；`CHATS.AWAITING_*` 只记录当前等待状态，不再为 `awaiting.ask` 写 event line。
- 有 awaiting 的 tool-call 批次，确认前不会产生 sibling `tool.result`；确认后所有结果落到同 `seq` 的 `_type:"react-tool"` continuation，且每个 `tool_call.id` 在下一次模型调用前必须恰好对应一个 `role:"tool"` result。
- run 在 awaiting 期间被 interrupt 时，后端按 `awaiting.answer -> tool.result(s) -> run.cancel` 发布。`awaiting.answer` 使用 `status:"error"`、`error.code/reason:"run_interrupted"`；所有可证明尚未执行的等待调用及其 queued sibling 都生成一个 matching result，正文固定为 `{"error":"run_interrupted","exitCode":-1,"output":"...","executed":false,"awaitingId":"..."}`。这里不会伪造 `decision:"reject"` 或 `role:"user"` approval。
- `executed:false` 只表示平台能证明调用尚未开始。已经进入普通工具执行或并发执行批次、但没有确定结果的调用不补 synthetic result，后续回放返回 `chat_history_incomplete`。
- interrupt 取消整个 Team 根 run；steer 写入协调器 run，不直接定向某个正在等待的成员。
- run-env checkpoint 随正常完成、失败、interrupt、budget stop 或恢复终态统一清理；终态后的 state 拒绝新访问。

## 相关文件

- `internal/hitl/`
- `internal/hitl/normalize.go`
- `internal/llm/run_stream_hitl_submit.go`
- `internal/llm/run_stream_hitl_shell.go`
- `internal/server/submit_validation.go`
- `internal/server/deferred_awaiting.go`
- `internal/server/restart_awaiting.go`
- `docs/手工测试用例.md`
