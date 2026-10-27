# Independent runs

Use chat_start, chat_get_status, chat_interrupt and chat_steer in an ordinary native main root Run with builtin.task-control mounted. These tools take top-level arguments, not `{action,args}`.

In user requests, conversation, dialogue and chat are synonyms for Chat, including their Chinese equivalents. A request to open, create or start a new or separate conversation means chat_start without chatId. It is not an agent_invoke or agent_delegate child call.

## chat_start

- kbase references: optional required library IDs for the new Run. Empty or omitted means no temporary requirement. The target Agent must mount builtin.kbase-control; its interactionConfig entry switches do not apply to chat_start; all selected libraries are revalidated at admission. This selection is independent of the parent Run.


`{message, agentKey?, chatId?, accessLevel?, references?, chatName?, modelKey?, reasoningEffort?}` starts an independent root run for a catalog Agent and returns chatId and runId immediately. It does not wait for the target result, and the target continues if the caller run is interrupted.

- message: required non-empty string. The task message sent to the target.
- agentKey: optional non-empty string holding the exact catalog Agent key. Omission uses the current calling Agent from trusted runtime context. Explicit empty, null or non-string values are rejected.
- Current Agent: omit agentKey, or set it to the exact Agent Identity.key from the current system prompt. References to the current Agent, this Agent or yourself in any language always mean that Agent; never substitute another Agent’s key from catalog discovery.
- chatId: non-empty string. Omit it to create a new Chat. Provide it only when the user asks to continue a specific existing Chat owned by the selected target. Omitting agentKey still selects the current calling Agent and requires that Chat to have the same owner. Never reuse the current chatId for a request to open a new Chat.
- chatName: non-empty string naming a new Chat. Pass it only when the user specifies a name; otherwise normal naming applies. Cannot be combined with chatId. taskName is not supported.
- accessLevel: `"default"`, `"auto_approve"` or `"full_access"`, for this run only. Pass it only when the user explicitly requests a permission level. Omission inherits the parent Run's current level at invocation time and does not follow later changes. Automatic approval means auto_approve, not full_access; destructive operations may still require approval. The target's interactionConfig switches do not restrict chat_start; the parent-level comparison and permission review below still apply.
- references: array of resource locators `{type,id}`. `type` is skill, kbase or chat; `id` is the exact resource ID. Skills are ordinary Skills, never connector skills. Each reference is used for the task. Empty or omitted means none. Chat references may only name Chats the calling Run can already read; an anonymous caller cannot create a new Chat grant through this tool. References grant no additional tools or access level.
- modelKey: non-empty string holding a registered model key, for this run only. Pass it only when the user asks for a specific model; when the target is the current Agent, omission inherits the calling Run's effective model; for any other Agent, omission uses that Agent's own configured model. It is the `modelKey` returned by chat_query `models`, not a provider model ID; never guess a key. Separate calls may use different models, and continuing a Chat with chatId may use a different model than its earlier runs.
- reasoningEffort: `"NONE"`, `"LOW"`, `"MEDIUM"`, `"HIGH"`, `"XHIGH"` or `"MAX"`, for this run only. `"NONE"` disables reasoning. Pass it only when the user asks for it; when the target is the current Agent, omission inherits the calling Run's effort (NONE when disabled); for any other Agent, omission uses that Agent's own default effort. Can be used without modelKey.

Examples:

```json
{"message": "Summarize yesterday's incidents"}
```

```json
{"message": "Review this design", "modelKey": "<modelKey from chat_query models>", "reasoningEffort": "HIGH"}
```

```json
{"message": "Continue with the second chapter", "agentKey": "writer", "chatId": "<existing chatId>"}
```

### Permission review

Levels are ordered default < auto_approve < full_access and compared with the calling Run's current level when the call is accepted.

- At or below the calling Run's level, or omitted: the run starts directly.
- Above it: the user must approve this one start in a review shown in the current Chat. No access level, approval rule or argument can grant it, and the calling Run's own level never changes. Waiting uses the normal approval timeout; a timeout counts as a rejection.
- Rejected, dismissed, timed out or interrupted: nothing was started. Do not call chat_start again for the same task, and never omit or lower accessLevel to avoid the review. Report the outcome to the user.
- An error with `retryable: true` (`run_start_review_stale`, `run_start_approval_required`, `run_parent_access_level_changed`) means nothing was started because the calling Run's level changed. Make a new chat_start call with the same target, message and accessLevel; a new review is shown when one is still required.
- `run_start_outcome_unknown` means the start could not be confirmed. Check the returned runId with chat_get_status; do not start again.

Errors raised before a run exists carry `executionState: "not_started"`.

## chat_get_status

Use either `{mailId}` or `{runId}`, never both. mailId reads the durable mail status; only delivery=started may include its owned Run state. Inserted letters expose only mail state. Legacy one-level restrictions still apply to this tool.

`{runId}` reads the current status and result snapshot of a run previously created by chat_start for this calling Agent and subject. runId is the required string returned by chat_start; it identifies one execution within a Chat, not the Chat itself.

When the run awaits human approval, tell the user to handle it in the target Chat instead of polling repeatedly. This tool cannot submit approvals.

## chat_interrupt

`{runId, message?}` interrupts such a run. message is an optional string with the reason or detail attached to the interrupt request. Interrupting does not close or delete the Chat.

## chat_steer

`{runId, message}` sends updated instructions to an independent run previously created by chat_start for the same calling Agent and subject. Both fields are required non-empty strings. No other arguments are accepted.

- runId identifies the target execution, not the Chat. A later ordinary root Run of the same initiating Agent and subject can steer it while active.
- message updates the task goal, scope or priority. It does not change tools, model, accessLevel or editing permissions. The tool needs no approval and does not compare caller and target permission levels. Existing target tool approvals still apply.
- The result has action `steer`, accepted, status, detail, run and steerId. The handler derives steerId deterministically from the calling runId and toolId. Find the same ID in the target Chat's request.steer event to observe handling; accepted alone does not mean the target has consumed the input or completed the task. No extra consumption acknowledgment exists.
- Every first invocation outcome is frozen, including accepted:false and executionState:unknown. Replaying the same invocation and arguments returns that outcome without sending again; changed arguments return idempotency_conflict. Unlike chat_start, a definite rejection is not retried within that invocation. The first outcome remains while the caller Run is active and is released after that Run ends; an in-flight call is retained until its result is published. Cancelling a replay only stops waiting and never replaces the original result; the original outcome is unknown while it is still pending. Idempotency is process-local, not a guarantee across restart. A new tool invocation is a new operation; do not automatically resend an uncertain outcome.
- A WS message that was not queued, a pre-send validation failure or an explicit upstream rejection returns executionState:not_started; a new tool invocation can retry a definite rejection. SSE transport failures, timeouts or unparseable responses return executionState:unknown. Source is not sent upstream. Inspect the target before deciding whether another instruction is needed; never automatically resend an uncertain outcome.
- Native inputs keep role user and unchanged message text. Optional event/JSONL source records type agent, agentKey, chatId, runId and toolId from trusted Runtime caller context. HTTP/WS input cannot supply source. The metadata is not sent to the model.
- Current model/tool work finishes normally before consumption. An active root wait can wake; active native planning can be superseded and replanned, still requiring user confirmation. Ordinary question/approval/form waits are not answered by steer. Suspended waits after restart still need submit.
- Ended or closed runs return accepted:false. This tool never starts another Run or continues a Chat automatically. To continue an ended Chat, explicitly use chat_start with chatId. After planning approval creates an execution Run, use its new runId.
- TEAM receives instructions in its coordinator root; this tool does not address members or revoke work already started.
- BTW read-only policy rejects this tool. Shipped planning-mode.exclude-tools excludes it from the caller's planning Run; this is independent of target planning state.

Example: `{"runId":"<runId returned by chat_start>","message":"Prioritize backend authorization and access tests; defer the management UI."}`

## Boundaries

- chat_get_status, chat_interrupt and chat_steer accept only runs created by chat_start for the same calling Agent and subject. They cannot address the current run, arbitrary runs, or agent_invoke/agent_delegate child tasks.
- A run created by chat_start cannot call any of these four tools.
- After a transport timeout or unknown outcome from chat_start, inspect existing Chats with chat_query before starting again; a repeated call starts another run.
