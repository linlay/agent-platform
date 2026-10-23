package platformcontrol

import (
	"context"
	"strings"
	"testing"

	"agent-platform/internal/viewquery"
)

func TestViewQueryOperationsKeepToolValidationWithoutAdmission(t *testing.T) {
	operations := (&ToolHandler{}).ViewQueryOperations()
	ctx, qc := context.Background(), viewquery.Context{ChatID: "c", RunID: "r"}
	// No ExecutionContext exists: list reads run without tool admission.
	result, err := operations["catalog.list"](ctx, qc, map[string]any{"resourceType": "agent"})
	if err != nil || result.(map[string]any)["total"] != 0 {
		t.Fatalf("catalog list: %#v %v", result, err)
	}
	for _, params := range []map[string]any{
		{"resourceType": "model"},
		{"resourceType": "agent", "status": "broken"},
		{"resourceType": "agent", "extra": true},
	} {
		if _, err := operations["catalog.list"](ctx, qc, params); err == nil {
			t.Fatalf("catalog params accepted: %#v", params)
		}
	}
	if _, err := operations["automation.list"](ctx, qc, map[string]any{"limit": float64(500)}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("automation limit: %v", err)
	}
	if _, err := operations["automation.list"](ctx, qc, map[string]any{}); err == nil || !strings.Contains(err.Error(), "automation service unavailable") {
		t.Fatalf("automation service: %v", err)
	}
}
