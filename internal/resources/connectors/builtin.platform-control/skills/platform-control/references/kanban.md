Use `desktop_kanban` with `{action,args}`.

# Kanban

Use these actions for Desktop Kanban issue CRUD and move operations.

## Execution

- To create for automatic execution, set `args.input.status: "todo"` and the actual `assigneeAgentKey`. With Kanban enabled, eligible local `todo` issues auto-run; scheduled automation uses its own trigger.
- Omitted status defaults to `backlog`, which does not auto-run. Report returned `backlog` as created and awaiting scheduling, never as ready or dispatched. For eligible `todo`, a null `runId` can reflect asynchronous admission.
- Desktop owns execution, results, and workflow progression. Do not launch duplicate runs, write results into `description`, or manually move issues to simulate progress or completion. Read progress with `kanban.getIssue`.
- Use updates/moves for requested edits or workflow changes. Send only changed fields: resetting `status` to `todo` can trigger another run.

## Actions

```text
kanban.listIssues [read]
kanban.getIssue [read]
kanban.createIssue [execute]
kanban.updateIssue [execute]
kanban.deleteIssue [execute]
kanban.moveIssue [execute]
```

## Arguments

- `kanban.getIssue` and `kanban.deleteIssue`: pass `{ "id": "issue-id" }`.
- `kanban.createIssue`: pass `{ "input": KanbanIssueInput }`.
- `kanban.updateIssue`: pass `{ "id": "issue-id", "input": KanbanIssueUpdateInput }`.
- `kanban.moveIssue`: pass `{ "id": "issue-id", "status": KanbanStatus, "position": 0, "baseIssueRevision": 12 }`; `baseIssueRevision` is optional.
- If the runtime is not initialized, Kanban actions return `kanban_unavailable`.

## Examples

```json
{
  "action": "kanban.createIssue",
  "args": {
    "input": {
      "title": "<task-title>",
      "status": "todo",
      "assigneeAgentKey": "<agent-key>"
    }
  }
}
```

Replace placeholders with the requested title and an actual Agent key from the current context or catalog.

## Local Issue Input And Status

These mutations manage local issues. Cloud issues are server-authoritative and are not writable through these actions.

For ordinary creation, `args.input.title` is a required non-empty string. Common optional inputs:

| Field inside input | Meaning |
| --- | --- |
| `description` | Issue text |
| `status` | Explicit column key; omitted defaults to `backlog` |
| `assigneeAgentKey` | Executor Agent key from the current context or catalog |
| `projectId` | Existing project ID from listIssues |
| `localWorkflowId` | Optional ID from listIssues.localWorkflows; do not copy an issue's workflowId |
| `priority` / `severity` | Priority P0–P3 / severity critical/high/medium/low |

`updateIssue` accepts partial `input`; omitted fields retain their values. To assign an existing issue, use `args: {"id":"<issue-id>","input":{"assigneeAgentKey":"<agent-key>"}}`.

| Status | Desktop column |
| --- | --- |
| `backlog` | 待排期 |
| `todo` | 待办 |
| `in_progress` | 进行中 |
| `in_review` | 评审中 |
| `completed` | 已完成 |

When the user asks for 待办, explicitly create with `status: "todo"`. Do not invent `pending`, `to_do`, `ready`, or `blocked`. Moving uses top-level `args.id/status/position`, not `args.input` and not `issueId`. `position` must be a finite JSON number, not a numeric string. `baseIssueRevision`, when supplied, is a non-negative integer.

Response fields `workflowId`, `typeId`, and `position` are not create/update input fields. Use `localWorkflowId` for an optional local workflow and `moveIssue` for position. Returned identity, timestamps, cloud state, and execution results must not be copied back as a creation template.

For `invalid_args`, use the returned `issues[].path` and expected type to correct the exact field. A missing `args.input` means the literal key `input` is required; `issue` is not an alternative. Read this reference before retrying and validate one corrected call before repeating it across a batch.
