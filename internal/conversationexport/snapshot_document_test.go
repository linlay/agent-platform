package conversationexport

import (
	"encoding/json"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
)

func TestSnapshotV1PreservesTimelineAndDeclaredAttachments(t *testing.T) {
	const base int64 = 1_790_000_000_000
	events := []stream.EventData{
		{Type: "request.query", Timestamp: base + 1, Payload: map[string]any{"runId": "run-1", "message": "Report"}},
		{Type: "run.start", Timestamp: base + 10, Payload: map[string]any{"runId": "run-1"}},
		{Type: "reasoning.snapshot", Timestamp: base + 20, Payload: map[string]any{"runId": "run-1", "reasoningId": "reason-1", "text": "Thinking", "reasoningLabel": "analysis"}},
		{Type: "tool.start", Timestamp: base + 25, Payload: map[string]any{"runId": "run-1", "toolId": "tool-1", "toolName": "bash"}},
		{Type: "tool.args", Timestamp: base + 26, Payload: map[string]any{"runId": "run-1", "toolId": "tool-1", "delta": `{"command":"date"}`}},
		{Type: "tool.snapshot", Timestamp: base + 30, Payload: map[string]any{"runId": "run-1", "toolId": "tool-1", "toolName": "bash", "arguments": `{"command":"date"}`}},
		{Type: "tool.result", Timestamp: base + 40, Payload: map[string]any{"toolId": "tool-1", "result": "done"}},
		{Type: "artifact.publish", Timestamp: base + 50, Payload: map[string]any{"runId": "run-1", "artifacts": []any{map[string]any{"name": "legacy.html", "url": "artifacts/run-1/legacy.html", "mimeType": "text/html"}}}},
		{Type: "content.snapshot", Timestamp: base + 60, Payload: map[string]any{"runId": "run-1", "contentId": "final", "text": "[报告](artifacts/run-1/%E6%8A%A5%E5%91%8A.html)"}},
		{Type: "run.complete", Timestamp: base + 456000, Payload: map[string]any{"runId": "run-1"}},
	}
	attachments := []AttachmentV1{{
		ID: "0123456789abcdef01234567", Name: "报告.html", MIMEType: "text/html",
		Size: 12, SHA256: "abcdef", SourceRef: "artifacts/run-1/%E6%8A%A5%E5%91%8A.html",
	}}
	document, err := BuildSnapshotDocument(&chat.Summary{ChatID: "chat-1", ChatName: "Report", CreatedAt: base}, events, attachments, base+456001, "zh-CN", nil)
	if err != nil {
		t.Fatal(err)
	}
	if document.Snapshot.Version != SnapshotVersion || len(document.Snapshot.Turns) != 1 || len(document.Snapshot.Attachments) != 1 {
		t.Fatalf("snapshot is incomplete: %+v", document.Snapshot)
	}
	turn := document.Snapshot.Turns[0]
	if turn.QueryAt != base+1 || *turn.EndedAt != base+456000 || len(turn.Nodes) != 4 ||
		turn.Nodes[2].ResultText != "done" || turn.Nodes[2].ArgsText != `{"command":"date"}` || turn.Nodes[2].DurationMs != 15 {
		t.Fatalf("run facts are incorrect: %+v", turn)
	}
	if document.Snapshot.Attachments[0].Name != "报告.html" || document.Snapshot.Attachments[0].SourceRef != "artifacts/run-1/%E6%8A%A5%E5%91%8A.html" || document.Snapshot.Attachments[0].Size != 12 || document.Snapshot.Attachments[0].SHA256 != "abcdef" {
		t.Fatalf("published HTML missing: %+v", document.Snapshot.Attachments)
	}
	var decoded map[string]any
	if err := json.Unmarshal(document.JSON, &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotV1IncludesResultWithoutToolStart(t *testing.T) {
	const base int64 = 1_790_000_000_000
	events := []stream.EventData{
		{Type: "request.query", Timestamp: base + 1, Payload: map[string]any{"runId": "run-1", "message": "Run"}},
		{Type: "tool.result", Timestamp: base + 2, Payload: map[string]any{"runId": "run-1", "toolId": "tool-1", "toolName": "bash", "result": map[string]any{"ok": true}}},
		{Type: "run.complete", Timestamp: base + 3, Payload: map[string]any{"runId": "run-1"}},
	}
	document, err := BuildSnapshotDocument(&chat.Summary{ChatID: "chat-1", CreatedAt: base}, events, nil, base+4, "zh-CN", nil)
	if err != nil {
		t.Fatal(err)
	}
	nodes := document.Snapshot.Turns[0].Nodes
	if len(nodes) != 2 || nodes[1].Kind != "tool" || !nodes[1].ResultIsCode || nodes[1].Status != "success" {
		t.Fatalf("result-only tool lost: %+v", nodes)
	}
}
