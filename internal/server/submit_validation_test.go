package server

import (
	"strings"
	"testing"
)

func TestValidateDeferredSubmitParamsAcceptsDismissAndValidShapes(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		params any
	}{
		{name: "question dismiss", mode: "question", params: []map[string]any{}},
		{name: "question answer", mode: "question", params: []map[string]any{{"answer": "Approve"}}},
		{name: "approval decision", mode: "approval", params: []map[string]any{{"decision": "approve"}}},
		{name: "form payload", mode: "form", params: []map[string]any{{"payload": map[string]any{"days": 2}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeferredSubmitParams(tt.mode, mustEncodeSubmitParams(t, tt.params))
			if err != nil {
				t.Fatalf("validateDeferredSubmitParams returned error: %v", err)
			}
		})
	}
}

func TestValidateDeferredSubmitParamsRejectsInvalidShape(t *testing.T) {
	err := validateDeferredSubmitParams("form", mustEncodeSubmitParams(t, []map[string]any{
		{"payload": "bad"},
	}))
	if err == nil || !strings.Contains(err.Error(), "items[0]: form payload must be an object") {
		t.Fatalf("unexpected error: %v", err)
	}
}
