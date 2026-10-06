---
name: task-control
description: Manage conversations, start and follow independent Agent or Team runs, and create or maintain automation schedules.
---

# Task Control

Requires builtin.task-control mounted in an ordinary Native root Run. Use chat_start to execute a task now and automation_manage to schedule future execution. Read [conversations](references/chat.md) for history and lifecycle operations, and [automation](references/automation.md) before changing schedules.

- chat_start starts an independent root run and returns chatId/runId immediately. Omit chatId for a new Chat; provide it only to continue an existing target-owned Chat. If no target is specified, use Agent Identity.key. Exactly one of agentKey/teamId is required. The target continues when the caller is interrupted.
- chat_get_status reads the status/result of a run previously created by chat_start for this calling Agent and subject; chat_interrupt interrupts that run. Both take runId, not chatId. They cannot control arbitrary or current runs, or agent_invoke/agent_delegate children. Runs created by chat_start cannot chain these three execution tools.
- chat_query reads conversations, visible history and artifacts. Its current action reads the current Chat; use chat_get_status to follow a launched run.
- chat_manage renames, pins, archives, restores, forks, exports or deletes Chats. Fork copies history; it does not submit a new task. Use chat_start to execute in the resulting Chat.
- automation_query reads schedules and execution history or validates a candidate. automation_manage creates, updates, enables, pauses, deletes or triggers schedules. Automations remain deployment-wide resources, not isolated by Agent.

Planning permits read-only operations only. Chat and automation deletion always require explicit human approval. Automation create/update/enable/trigger use Platform review in default mode and allow audited automatic approval in auto_approve/full_access. Saved query.accessLevel controls future scheduled runs independently of the current Chat; omission uses default. Never bypass rejected management operations through files or shell commands. Unknown mutation outcomes require inspecting state before retrying.

Task Control does not grant webpage access, a Desktop connection, or Kanban management. Mount web-control separately for webpage tasks.
