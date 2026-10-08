# Automation workflow

Use this workflow for automations, schedules, scheduled tasks, 定时任务, 计划任务 and 提醒. Tool arguments and approval rules are in [automation](automation.md); definition files are in [YAML](automation-yaml.md).

## Intent and executor

1. Identify whether the user wants to create, inspect, change, enable, pause, trigger or delete a task. Ask for missing time information only when creating or changing its schedule. A request to list, pause or delete an identified task needs no new time.
2. Unless the user specifies an executor, use the current Agent. Obtain its exact key from Agent Identity. A different Agent or Team requires an explicit request and an actual available key or ID; do not substitute a candidate automatically. Use catalog_query only when that tool is available. If an explicit target cannot be resolved, ask for the missing identity.
3. Preserve the requested action, object and necessary context in query.message. A request to remind the user must remain a reminder; a request to execute, check or generate something must retain that action. This is a general intent rule, not a list of topic-specific behaviors.
4. Make the future instruction self-contained. Resolve missing inputs during creation rather than scheduling a task that needs the user to explain its original request again.
5. Omit query.chatId by default. Set it only when the user explicitly asks to continue or deliver into a particular Chat, including the current one. Never bind a task just because its creation request came from that Chat.
6. Saved query.accessLevel controls scheduled and manual execution. Its default is default, independently of the creating Run and the target Chat's previous permission level. Specify another level only when the user requests it and target admission permits it.

## Time and Cron

Resolve relative dates and times against the current time in the effective business timezone, then calculate an absolute intended time. Omitted timezone follows the platform scheduling timezone; an explicit IANA zone fixes it. State the interpreted date and zone when ambiguity can be resolved from context; ask when the interpretation would materially change the request.

Cron has exactly five fields: minute, hour, day-of-month, month, day-of-week. It has no seconds or year field. Do not promise second precision. Do not invent a sixth field or assume a date written in description affects scheduling.

| Requested schedule | Cron form |
| --- | --- |
| Daily at HH:MM | `MM HH * * *` |
| Monday at HH:MM | `MM HH * * 1` |
| Weekdays at HH:MM | `MM HH * * 1-5` |
| One intended calendar occurrence | `MM HH DD MO *`, with remainingRuns: 1 |

For a one-time task, include its absolute intended date and time in description and calculate the next Cron occurrence. It must match the user's intended date, time and year. A past date can resolve to next year; a distant date can resolve to an earlier year. Neither is an acceptable substitute. If five-field Cron cannot express the requested occurrence, report that limitation rather than saving a misleading task.

Minute steps restart within the hour. `*/7 * * * *` means minute 0, 7, 14, …, 56 of each hour; the transition from 56 to the next hour's 0 is four minutes. Do not equate every `*/N` expression with an uninterrupted N-minute interval. Likewise, validate day-of-month/day-of-week combinations using the actual Cron preview rather than assuming both restrictions intersect.

Use datetime, when available, for current time and conversion. Validate the completed candidate with automation_query.validate and inspect its preview, especially for one-time tasks, month/year boundaries, timezone changes and daylight-saving transitions. A preview is a calculated schedule, not an execution guarantee or a missed-run recovery promise.

## Change and verify

- Read the existing task and its baseRevision before changing it. Resolve ambiguous matches before mutation; preserve fields unrelated to the request.
- Changing a limited task into an unlimited periodic task uses remainingRuns:null in update. Changing it into a one-time task sets remainingRuns:1 and validates the intended next occurrence.
- Creation and later edits must preserve the selected executor unless the user asks to change it. Preserve an existing Chat binding unless its change is requested.
- After management, inspect the returned definition; use list to inspect the active nextFireAt when available. For file maintenance, also follow the source and loading checks in the YAML reference.
- Distinguish saved, scheduled, accepted, completed and delivered. Manual trigger returns accepted/executionId; inspect execution history for completion. A scheduled query or a Chat result does not by itself establish delivery through a separate notification channel.
- Scheduled triggers consume remainingRuns before dispatch, including failed dispatch. At zero the scheduler removes the definition. Manual trigger does not consume remainingRuns. Explain these semantics when they affect the requested change.

Report the task identity, requested action, one-time or periodic schedule, interpreted time and zone, executor, explicit Chat binding if any, and verified outcome. Do not claim the task ran merely because its definition was saved.
