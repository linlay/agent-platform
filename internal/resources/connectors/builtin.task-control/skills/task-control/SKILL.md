---
name: task-control
description: Manage conversations, start and follow independent Agent or Team runs, and create or maintain automations, scheduled tasks and reminders.
---

# Task Control

Requires builtin.task-control mounted in an ordinary Native root Run. Use chat_start to execute a task now and automation_manage to schedule future execution.

Automation, schedule, scheduled task, 定时任务, 计划任务 and 提醒 describe the same scheduling capability. Read [automation workflow](references/automation-workflow.md) when interpreting a scheduling request. Read [automation YAML](references/automation-yaml.md) for definition files and authorized source maintenance.

Tool definitions only name a capability. Read the reference for a tool before calling it; arguments, types, constraints and examples live there, not in the tool schema.

| Tools | Reference |
| --- | --- |
| chat_start, chat_get_status, chat_interrupt, chat_steer | [independent runs](references/run.md) |
| chat_query, chat_manage | [conversations](references/chat.md) |
| automation_query, automation_manage | [automation](references/automation.md) |

- chat_start starts an independent root run and returns chatId/runId immediately. Omit chatId for a new Chat; provide it only to continue an existing target-owned Chat. Omit agentKey to use the current calling Agent; an explicit agentKey selects that exact catalog Agent.  The target continues when the caller is interrupted.
- chat_get_status reads the status/result of a run previously created by chat_start for this calling Agent and subject; chat_interrupt interrupts that run. Both take runId, not chatId. They cannot control arbitrary or current runs, or agent_invoke/agent_delegate children. Runs created by chat_start cannot chain these four execution tools.
- chat_steer sends non-empty updated instructions to an owned active run, taking runId and message. It needs no approval and does not compare permission levels. It returns the first invocation outcome even when rejected; do not replay to attempt enqueue again. Its deterministic steerId links the result to the target request.steer event.
- chat_start needs only message. Omitted agentKey/modelKey/reasoningEffort use the current Agent and Run; explicit values override individually. List model keys with chat_query `models`.
- chat_query reads conversations, visible history and artifacts, and lists selectable models. Its current action reads the current Chat; use chat_get_status to follow a launched run.
- chat_manage renames, pins, archives, restores, forks, exports or deletes Chats. Fork copies history; it does not submit a new task. Use chat_start to execute in the resulting Chat.
- automation_query reads schedules and execution history or validates a candidate. automation_manage creates, updates, enables, pauses, deletes or triggers schedules. Automations remain deployment-wide resources, not isolated by Agent.

Planning permits read-only operations only. Chat and automation deletion always require explicit human approval. Automation create/update/enable/trigger use Platform review in default mode and allow audited automatic approval in auto_approve/full_access. Saved query.accessLevel controls future scheduled runs independently of the current Chat; omission uses default. Never bypass rejected management operations through files or shell commands. Unknown mutation outcomes require inspecting state before retrying.

Task Control does not grant webpage access, a Desktop connection, or Kanban management. Mount web-control separately for webpage tasks.
