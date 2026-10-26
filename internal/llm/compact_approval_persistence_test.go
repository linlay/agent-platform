package llm

import (
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/models"
	"agent-platform/internal/stream"
)

func TestActiveSummaryAfterAutoApprovalMatchesStoredNotice(t *testing.T) {
	root := t.TempDir()
	store, err := chat.NewFileStoreAtStartup(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const id = "chat-compact-approval"
	timestamp := int64(1792994468000)
	for _, run := range []string{"old", "current"} {
		if err := store.AppendQueryLine(id, chat.QueryLine{Type: "query", ChatID: id, RunID: run, UpdatedAt: timestamp, Messages: []map[string]any{{"role": "user", "content": run + " instruction", "ts": timestamp}}}); err != nil {
			t.Fatal(err)
		}
	}
	writer := chat.NewStepWriter(store, id, "current", "GENERAL")
	writer.OnEvent(stream.EventData{Type: "tool.snapshot", Timestamp: timestamp, Payload: map[string]any{"toolId": "approved-call", "toolName": "file_read", "arguments": `{"filePath":"example.txt"}`}})
	s := &llmRunStream{
		model:              models.ModelDefinition{ContextWindow: 204800},
		onApprovalSummary:  writer.RecordApproval,
		pinnedMessageStart: 1, pinnedMessageEnd: 2,
		messages: []openAIMessage{
			{Role: "user", Content: "old instruction"},
			{Role: "user", Content: "current instruction"},
			{Role: "assistant", ToolCalls: []contracts.ModelToolCall{{ID: "approved-call", Type: "function", Function: contracts.ModelFunctionCall{Name: "file_read", Arguments: `{"filePath":"example.txt"}`}}}},
		},
	}
	s.appendToolResultMessageOrdered(&preparedToolInvocation{toolID: "approved-call", toolName: "file_read", args: map[string]any{"filePath": "example.txt"}, fileAccessPlan: &filetools.AccessPlan{Path: "example.txt", CommandText: "file_read example.txt", Mode: filetools.ReadAccess}, hitlDecision: &hitlDecisionState{Decision: "auto_approved", Reason: "accessLevel=auto_approve", Mode: "approval"}}, contracts.ToolExecutionResult{Output: `{"ok":true}`})
	// The producer records the approval before the consumer receives tool.result.
	// Consuming that event first flushes the assistant call as a separate row.
	writer.OnEvent(stream.EventData{Type: "tool.result", Timestamp: timestamp, Payload: map[string]any{"toolId": "approved-call", "toolName": "file_read", "result": map[string]any{"ok": true}}})
	writer.Flush()
	if err := writer.Err(); err != nil {
		t.Fatal(err)
	}
	plan := s.buildContextCompactPlan(false)
	messages := append([]map[string]any{{"role": "user", "content": chat.CompactCheckpointSummaryMessage("summary")}}, modelMessagesToMaps(append(plan.pinned, plan.retained...))...)
	err = store.AppendRunCompactCheckpoint(id, chat.RunCompactCheckpointLine{Type: chat.RunCompactCheckpointLineType, Level: "summary", CompactID: "approval-summary", RunID: "current", UpdatedAt: timestamp, Messages: messages, CompactCoveredMessages: modelMessagesToMaps(plan.candidates)})
	if err != nil {
		t.Fatalf("commit active summary after auto approval: %v", err)
	}
	raw, err := store.LoadRawMessages(id, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 5 || raw[len(raw)-1]["content"] != s.messages[len(s.messages)-1].Content {
		t.Fatalf("approval notice missing after reload: messages=%d", len(raw))
	}
}
