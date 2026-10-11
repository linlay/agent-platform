# Automation YAML

Use Runtime Context's automations_dir for editable definitions. Existing files may end in .yml or .yaml; the filename stem is the automation ID. Do not guess the runtime root or edit generated ru-agents/ru-connectors directories. Use [management tools](automation.md) for ordinary changes, and this reference for explicitly authorized source maintenance with available file tools and path permissions. Never use files or shell commands to bypass a rejected management operation.

## Definition

Required fields are name, cron, agentKey, and query.message. Description is optional; the loader imposes no fixed first-two-lines order or single-line description rule. Unless the user specifies another executor, populate agentKey with the current Agent's exact key.

| Field | Contract |
| --- | --- |
| name | Non-empty task name |
| description | Optional explanation; for a one-time task, disclose the absolute intended time |
| enabled | Boolean; omitted means true |
| cron | Traditional five-field Cron; see the [time rules](automation-workflow.md) |
| remainingRuns | Positive integer; omitted means unlimited; one-time execution uses 1 |
| agentKey | Exact root Agent identity, including TEAM |
| environment.zoneId | Optional IANA zone; omitted follows platform automation.default-zone-id, then process time.Local |
| query.message | Non-empty self-contained instruction; preserve its actual text, including newlines and whitespace |
| query.accessLevel | default, auto_approve or full_access; omitted means default, without Chat permission inheritance |
| query.chatId | Optional explicit Chat destination; do not infer it from the creating Chat |
| query.role | Optional valid query role; omitted executes as automation |
| query.hidden | Optional boolean; omitted means true, hiding the query message while Chat/Run and assistant replies remain visible |
| query.params | Optional object |
| query.requestId | Optional request identifier |
| query.references | Optional reference objects using the current Query reference contract |
| query.scene | Optional scene object using the current Query scene contract |

query.requestId/references/scene are supported in YAML but are not creation/update inputs of the management tools. Preserve such fields when maintaining an existing source; do not invent unsupported tool arguments. YAML uses environment.zoneId, whereas management input uses zoneId. Do not write old top-level zoneId/params or query.stream.

```yaml
name: Daily review
cron: "0 18 * * 1-5"
agentKey: <current-agent-key>
query:
  message: Review the day's work and produce a concise summary.
```

This example follows the platform timezone and uses default permissions. Add an explicit zone or access level only according to the user's request. For multiline message text, use a valid YAML scalar and verify that the decoded text matches the intended instruction.

## Source maintenance

1. List the exact automations_dir when the filename is unknown. Resolve the task using its name, executor and any explicit Chat binding, then read the whole definition before editing.
2. Preserve the existing ID and unrelated fields. For creation, choose a readable unique filename; verify absence rather than overwriting an existing task.
3. Validate Cron, timezone, executor, permission and intended next occurrence. The management validate action accepts its exposed candidate fields; it does not validate every advanced YAML field. A permitted managed source editor performs the full loader validation.
4. Save the smallest requested change, then read back and verify the decoded content. Direct file edits do not share management revision checks or invocation receipts and can race with scheduled remainingRuns writes; prefer the managed source editor or management tool when preserving a live limited task's baseline matters.
5. The automation watcher reloads definition changes asynchronously. Check the management definition and active schedule after reload before reporting that the change is scheduled. File contents, loaded definitions and execution records are different evidence.

For an existing limited task changed to unlimited, remove remainingRuns from YAML; do not write 0. Tool update instead uses remainingRuns:null. To pause, retain the definition with enabled:false; to enable, use true. Unspecified timezone remains unspecified in both YAML and tool writes; explicit timezone is fixed. Updating zoneId to an empty string in the tool removes the explicit setting.

Scheduler retirement and structured persistence may rewrite formatting, comments and field order. Do not use layout as a correctness check. Deletion/recovery must follow the currently supported management behavior; this reference does not grant an unimplemented recycle/restore action.
