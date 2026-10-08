# Independent runs

Use chat_start, chat_get_status and chat_interrupt in an ordinary native main root Run with builtin.task-control mounted. These tools take top-level arguments, not `{action,args}`.

In user requests, conversation, dialogue and chat are synonyms for Chat, including their Chinese equivalents. A request to open, create or start a new or separate conversation means chat_start without chatId. It is not an agent_invoke or agent_delegate child call.

## chat_start

`{message, agentKey?, teamId?, chatId?, accessLevel?, mustUseSkills?, chatName?}` starts an independent root run for exactly one catalog Agent or Team and returns chatId and runId immediately. It does not wait for the target result, and the target continues if the caller run is interrupted.

- message: required non-empty string. The task message sent to the target.
- agentKey / teamId: non-empty strings holding the exact catalog key or ID. Provide exactly one. If the user names no target, use the current Agent.
- Current Agent: set agentKey to the exact Agent Identity.key from the current system prompt. References to the current Agent, this Agent or yourself in any language always mean that Agent; never substitute a key from Runtime Context: Sub-Agent Candidates.
- chatId: non-empty string. Omit it to create a new Chat. Provide it only when the user asks to continue a specific existing Chat owned by the selected target. Never reuse the current chatId for a request to open a new Chat.
- chatName: non-empty string naming a new Chat. Pass it only when the user specifies a name; otherwise normal naming applies. Cannot be combined with chatId. taskName is not supported.
- accessLevel: `"default"`, `"auto_approve"` or `"full_access"`, for this run only. Pass it only when the user explicitly requests a permission level. Omission inherits the parent Run's current level at invocation time and does not follow later changes. Automatic approval means auto_approve, not full_access; destructive operations may still require approval. Target admission applies to all levels. See Permission review below.
- mustUseSkills: array of non-empty skill ID strings required for an ordinary Agent run. Empty or omitted means none. Team runs and connector skills do not support this selection.

Examples:

```json
{"message": "Summarize yesterday's incidents", "agentKey": "<Agent Identity.key>"}
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

`{runId}` reads the current status and result snapshot of a run previously created by chat_start for this calling Agent and subject. runId is the required string returned by chat_start; it identifies one execution within a Chat, not the Chat itself.

When the run awaits human approval, tell the user to handle it in the target Chat instead of polling repeatedly. This tool cannot submit approvals.

## chat_interrupt

`{runId, message?}` interrupts such a run. message is an optional string with the reason or detail attached to the interrupt request. Interrupting does not close or delete the Chat.

## Boundaries

- chat_get_status and chat_interrupt accept only runs created by chat_start for the same calling Agent and subject. They cannot address the current run, arbitrary runs, or agent_invoke/agent_delegate child tasks.
- A run created by chat_start cannot call any of these three tools.
- After a transport timeout or unknown outcome from chat_start, inspect existing Chats with chat_query before starting again; a repeated call starts another run.
