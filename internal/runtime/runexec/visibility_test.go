package runexec

import (
	"testing"

	"agent-platform/internal/stream"
)

func TestSystemInitQueryIsNotPublishedToClients(t *testing.T) {
	event := stream.EventData{
		Type: "request.query",
		Payload: map[string]any{
			"kind":   "system-init",
			"hidden": true,
			"system": map[string]any{"agentKey": "agent", "cacheKey": "react:main", "fingerprint": "sha256:test"},
		},
	}
	if stream.IsClientVisibleEventData(event) {
		t.Fatalf("system-init query must remain storage-only: %#v", event)
	}
	visible := ClientVisibleEventData(stream.EventData{
		Type: "request.query",
		Payload: map[string]any{
			"message": "hello",
			"system":  map[string]any{"secret": true},
		},
	})
	if _, ok := visible.Payload["system"]; ok || visible.String("message") != "hello" {
		t.Fatalf("client query filtering failed: %#v", visible)
	}
}

func TestInternalOnlyToolResultIsNotPublishedToClients(t *testing.T) {
	event := stream.EventData{
		Type: "tool.result",
		Payload: map[string]any{
			"toolId":       "tool-skipped",
			"internalOnly": true,
			"result":       `{"error":"tool_calls_exceeded","executed":false}`,
		},
	}
	if stream.IsClientVisibleEventData(event) {
		t.Fatalf("internal-only tool result must remain storage-only: %#v", event)
	}

	builder := NewFullTextBuilder()
	builder.Consume(event)
	if got := builder.Text(""); got != "" {
		t.Fatalf("internal-only tool result leaked into full text: %q", got)
	}
}
