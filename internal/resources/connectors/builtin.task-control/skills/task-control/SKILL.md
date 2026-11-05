---
name: task-control
description: Manage conversations, start and follow independent Agent or Team runs, and create or maintain automations, scheduled tasks and reminders.
---

# Task Control

Requires builtin.task-control mounted in an ordinary Native root Run. Prefer chat_send for durable correspondence and automation_manage for future execution. Use chat_send with newChat:true for independent work.

Automation, schedule, scheduled task, 定时任务, 计划任务 and 提醒 describe the same scheduling capability. Read [automation workflow](references/automation-workflow.md) when interpreting a scheduling request. Read [automation YAML](references/automation-yaml.md) for definition files and authorized source maintenance.

A message marked as an automation execution is the task of an existing automation running now. Being started by an automation is not a request to manage one: do the task, and do not create, change or trigger any automation from it.

A request to open a new/separate conversation, dialogue, chat, 会话 or 对话 routes to chat_send with newChat:true, not agent_invoke. Omitting both chatId and newChat sends to the primary Chat.

Tool definitions only name a capability. Read the reference for a tool before calling it; arguments, types, constraints and examples live there, not in the tool schema.

| Tools | Reference |
| --- | --- |
| chat_send, chat_notify, chat_submit | [mailbox](references/mailbox.md) |
| chat_get_status, chat_interrupt | [independent runs](references/run.md) |
| chat_query, chat_manage | [conversations](references/chat.md) |
| automation_query, automation_manage | [automation](references/automation.md) |

- chat_send sends a durable letter to a Chat. Use newChat:true for independent work; use chatId to supplement the same work, or omit both for the target Agent primary Chat. Every consumed letter receives the complete final Run output automatically. Use plain wait for replies; mail wakes it. New mail tools remain available in mail-started root Runs and are bounded by hops.
- chat_notify sends useful interim results to the opening letter sender; chat_submit answers delivered asks. Read the mailbox reference for submission shapes and approval limits.
- chat_get_status reads the status/result of a run opened by a letter for this calling Agent and subject; chat_interrupt interrupts that run. Both take runId, not chatId. They cannot control arbitrary or current runs, or agent_invoke/agent_delegate children. Letter-started Runs cannot query or interrupt other Runs.
- chat_query reads conversations, visible history and artifacts, and lists selectable models. Its current action reads the current Chat; use chat_get_status to follow a launched run.
- chat_manage renames, pins, archives, restores, forks, exports or deletes Chats. Fork copies history; it does not submit a new task. Use chat_send with its chatId to execute in the resulting Chat.
- automation_query reads schedules and execution history or validates a candidate. automation_manage creates, updates, enables, pauses, deletes or triggers schedules. Automations remain deployment-wide resources, not isolated by Agent.

Planning permits read-only operations only. Chat and automation deletion always require explicit human approval. Automation create/update/enable/trigger use Platform review in default mode and allow audited automatic approval in auto_approve/full_access. Saved query.accessLevel controls future scheduled runs independently of the current Chat; omission uses default. Never bypass rejected management operations through files or shell commands. Unknown mutation outcomes require inspecting state before retrying.

Task Control does not grant webpage access, a Desktop connection, or Kanban management. Mount web-control separately for webpage tasks.