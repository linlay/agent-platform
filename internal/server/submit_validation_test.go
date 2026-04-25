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
		{name: "form submit", mode: "form", params: []map[string]any{{"action": "submit", "form": map[string]any{"days": 2}}}},
		{name: "form reject", mode: "form", params: []map[string]any{{"action": "reject"}}},
		{name: "form cancel", mode: "form", params: []map[string]any{{"action": "cancel"}}},
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
	tests := []struct {
		name       string
		params     any
		wantSubstr string
	}{
		{
			name:       "missing action",
			params:     []map[string]any{{"form": map[string]any{"days": 2}}},
			wantSubstr: "items[0]: form items require action",
		},
		{
			name:       "invalid action",
			params:     []map[string]any{{"action": "approve", "form": map[string]any{"days": 2}}},
			wantSubstr: `items[0]: unsupported form action "approve"`,
		},
		{
			name:       "submit missing form",
			params:     []map[string]any{{"action": "submit"}},
			wantSubstr: "items[0]: submit action requires form",
		},
		{
			name:       "form not object",
			params:     []map[string]any{{"action": "submit", "form": "bad"}},
			wantSubstr: "items[0]: form field must be an object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeferredSubmitParams("form", mustEncodeSubmitParams(t, tt.params))
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
