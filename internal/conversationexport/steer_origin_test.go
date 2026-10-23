package conversationexport

import (
	"bytes"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/stream"
)

func TestExportPreservesSteerInstructionsAndSource(t *testing.T) {
	source := (&queryinput.Origin{Type: "agent", AgentKey: "caller", ChatID: "parent-chat", RunID: "parent-run", ToolID: "call"}).Payload()
	events := []stream.EventData{
		{Type: "request.query", Timestamp: testEpoch + 1, Payload: map[string]any{"runId": "run", "role": "user", "message": "original"}},
		{Type: "request.steer", Timestamp: testEpoch + 2, Payload: map[string]any{"runId": "run", "role": "user", "steerId": "steer-id", "message": "revised goal", "origin": source}},
		{Type: "content.snapshot", Timestamp: testEpoch + 3, Payload: map[string]any{"runId": "run", "text": "result"}},
		{Type: "run.complete", Timestamp: testEpoch + 4, Payload: map[string]any{"runId": "run"}},
	}
	doc, err := snapshotDocument(&chat.Summary{ChatID: "chat", CreatedAt: testEpoch}, events, nil, testEpoch+5, "zh-CN", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(doc.JSON, []byte(`"origin":`)) || !bytes.Contains(doc.JSON, []byte(`"steerId":"steer-id"`)) {
		t.Fatal(string(doc.JSON))
	}
	if n := doc.Snapshot.Turns[0].Nodes[1]; n.Origin == nil || n.Origin.RunID != "parent-run" || n.Text != "revised goal" || n.Role != "user" {
		t.Fatal(n)
	}
	md, err := RenderMarkdown(doc.Snapshot)
	if err != nil || !strings.Contains(string(md), "revised goal") || !strings.Contains(string(md), "parent-run") {
		t.Fatalf("%s %v", md, err)
	}
}
