package chat

import (
	"testing"

	"agent-platform/internal/stream"
)

func TestStepWriterApprovalWaitsForMatchingToolResult(t *testing.T) {
	store := newCompactTestStore(t)
	const id = "chat-approval-matching-result"
	writer := NewStepWriter(store, id, "run", "GENERAL")
	writer.RecordApproval(StepApproval{
		Summary: "approved", Notice: "approval notice",
		Decisions: []StepApprovalDecision{{ToolID: "approved", Decision: "auto_approved"}},
	})
	// Unrelated steps and the matching call's assistant row must not consume
	// an approval that belongs after the matching tool result.
	for _, toolID := range []string{"unrelated", "approved", "later"} {
		writer.OnEvent(stream.EventData{Type: "tool.snapshot", Timestamp: testEpochMillis(10), Payload: map[string]any{
			"toolId": toolID, "toolName": "file_read", "arguments": `{"filePath":"example.txt"}`,
		}})
		writer.OnEvent(stream.EventData{Type: "tool.result", Timestamp: testEpochMillis(11), Payload: map[string]any{
			"toolId": toolID, "toolName": "file_read", "result": "result",
		}})
		writer.Flush()
	}
	if err := writer.Err(); err != nil {
		t.Fatal(err)
	}
	raw, err := store.LoadRawMessages(id, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 7 {
		t.Fatalf("expected 3 call/result pairs and one notice, got %d", len(raw))
	}
	if raw[3]["tool_call_id"] != "approved" || raw[4]["role"] != "user" || raw[4]["content"] != "approval notice" {
		t.Fatal("approval notice was not persisted after its matching result")
	}
	if writer.pendingApproval != nil {
		t.Fatal("persisted approval was not consumed")
	}
}
