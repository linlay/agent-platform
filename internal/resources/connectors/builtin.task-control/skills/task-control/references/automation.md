# Automation

Use automation_query and automation_manage with `{action,args}`. Requires builtin.task-control mounted in an ordinary Native root Run. Definitions and histories are deployment-wide management resources. Planning stages allow queries only. Existing HTTP/Desktop management remains available through the shared Automation service.

Read [workflow](automation-workflow.md) for natural-language time and execution intent, and [YAML](automation-yaml.md) for source definitions.

## Read and preview

- list: `{limit?,offset?}`. limit is an integer 1–100, default 20. Follow nextOffset while hasMore.
- get: `{id}` returns automation and baseRevision. Read before every change or trigger.
- executions: `{id,limit?,offset?}` returns execution summaries and total. History may be unavailable without preventing task management or execution.
- execution: `{executionId}` returns full instructions and result for one execution.
- validate: complete create arguments below, without saving or approval. Returns valid, normalized automation and up to three calculated schedule times. These are schedule previews, not execution guarantees; paused tasks do not run automatically.

## Manage

- create: `{name,cron,query,agentKey,description?,zoneId?,enabled?,remainingRuns?}`. Unless the user specifies an executor, use the current Agent's exact key. Choose a different Agent or Team only when requested, using available context or catalog_query when mounted; task-control does not grant catalog_query. agentKey is required. cron uses five fields (minute hour day-of-month month day-of-week); zoneId is an IANA zone. enabled is boolean, default true. remainingRuns is an integer 1–100 in these tools; omit for unlimited. Query is `{message,accessLevel?,chatId?,role?,hidden?,params?}`; hidden is boolean. Message text is preserved exactly. Access defaults to default, regardless of the calling chat's access level. An omitted zone stays omitted in the definition and follows the configured scheduling zone, as with YAML. An explicit zone fixes the business timezone; approval and preview display the effective zone.
- update: `{id,baseRevision,...changed create fields}`. Omitted fields remain unchanged. Providing query replaces the exposed query fields, so read and preserve fields you want to keep. Omit remainingRuns to retain the existing limit, provide an integer 1–100 to set it, or provide `remainingRuns:null` to remove the limit without recreating the task. Providing an empty zoneId removes the explicit timezone and follows the platform zone; omitting zoneId keeps its existing setting.
- setEnabled: `{id,baseRevision,enabled}`. enabled is boolean, never a toggle.
- trigger: `{id,baseRevision}` executes the approved snapshot once, including paused tasks. Does not enable a task or consume remainingRuns. Returns accepted and executionId; accepted does not mean completed. Use execution to inspect the result.
- delete: `{id,baseRevision}` removes the definition and retains existing execution history and conversations.

All management operations require approval bound to exact invocation, content and baseline. Default mode displays a dedicated business review. auto_approve/full_access permits automatic approval except delete, which always requires a human. Task execution still follows saved query.accessLevel; no chat permission inheritance.

Saving/pause/delete updates scheduling registrations and can cancel an in-progress scheduled execution. Manually triggered executions are not canceled by those configuration changes. A task reaching its remainingRuns limit follows the existing scheduler retirement behavior.

## Conflicts and retries

A changed or removed definition causes revision_conflict. Read again and obtain new approval. Do not change parameters within the same tool invocation. Completed invocation receipts return the prior result without repeating creation or execution, including after a process restart. An interrupted intent has an unknown outcome and is not automatically retried: inspect the task and history before authorizing a new invocation. Receipts are stored in runtime state, not the automation definitions directory. Never bypass a rejected operation via Bash or file tools.
