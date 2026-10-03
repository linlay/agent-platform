# Conversations

Use chat_query and chat_manage in an ordinary native main root Run with builtin.platform-control mounted. Anonymous Runs can access only their current Chat; authenticated query-owned Chats remain owner restricted. Agent availability is not required for historical reads.

## Queries

- current: `{}` returns current summary and active Run state.
- list: `{scope?:"agent"|"instance", archived?, pinned?, limit?, cursor?}`. Default scope is current Agent. limit is 1–100 (default 20). Follow nextCursor even when a page is empty. Ordering is stable Chat ID order, independent of UI pins/manual sorting.
- search: `{query, scope?, chatId?, archived?, limit?, cursor?}` searches visible user/assistant text; returns up to 500-character snippets around matches. Follow nextCursor when incomplete; skippedChatIds identifies histories above the 8 MiB per-Chat scan limit. These are not proof of no matches.
- read: `{chatId, view:"summary"|"messages", archived?, limit?, cursor?}`. Messages exclude internal reasoning, system prompts and tool payloads. Long messages continue with offset/continued and nextCursor. Keep other query arguments unchanged. Restart if history changed.
- artifacts: `{chatId?, runId?, limit?, cursor?}` lists published artifact metadata through the existing authenticated Chat resource service. Follow nextCursor; no file body is returned.

## Management

- rename: `{chatId?, chatName}`; omitted Chat means current.
- setPinned: `{chatId?, pinned}`; pinning uses the shared instance-wide Chat ordering service.
- archive / restore: exactly one of `{chatId}` or `{chatIds:["id1","id2"]}`. Batches accept 1–100 distinct IDs, validate the request before mutation, then check ownership and execute each Chat independently. Return `total`, `succeeded`, `failed`, and `results` with `chatId`, `success`, `error` on failure and `executionState`. Continue after failures; successful changes are not rolled back. Retry only the failed IDs after inspecting their errors and any unknown execution state.
- fork: `{sourceChatId, sourceRunId?, chatName?}` creates an independent Chat using the existing derivation service.
- export: `{chatId, archived?, format:"markdown"|"snapshot"}` writes visible Markdown or the full standard Snapshot V1 timeline into the current Chat and returns a relative url. It does not publish the file or send it elsewhere.
- delete: `{chatId, archived?}` permanently deletes the Chat after exact one-time human approval, including in full_access. A changed Chat invalidates the review.

Archive, delete and fork refuse active or pending-HITL Chats, including the calling Chat. Run admission keeps its existing concurrency boundary. Fork/export bind persistent receipts to Run/tool ID and arguments; same invocation reuses the result, changed arguments conflict. A new tool invocation is a new operation. Snapshot exports include the existing timeline reasoning and tools; they are not raw JSONL or credential backups. An existing fork without completed provenance receipts returns idempotency_conflict; inspect it instead of overwriting it. Desktop preview of @chat files depends on its existing resource support.
