package runops

import (
	"context"
	"strings"
	"testing"
)

func TestRunControlRejectsMalformedInputBeforeLookup(t *testing.T) {
	h := NewToolHandler(newFakeRunToolService(), nil)
	for _, tool := range []string{StatusToolName, InterruptToolName} {
		for _, args := range []map[string]any{{}, {"runId": 123}, {"runId": "x", "secret-key": "secret-value"}, {"runId": nil}} {
			r, err := h.Invoke(context.Background(), tool, args, runToolExecContext("alice", "tool"))
			if err != nil || r.Error != "invalid_request" || r.Structured["executionState"] != "not_started" || !strings.Contains(r.Output, "expected") || strings.Contains(r.Output, "secret-") {
				t.Fatalf("%#v %v", r, err)
			}
		}
	}
	r, _ := h.Invoke(context.Background(), InterruptToolName, map[string]any{"runId": "x", "message": true}, runToolExecContext("alice", "tool"))
	if r.Structured["field"] != "message" {
		t.Fatal(r.Output)
	}
}
