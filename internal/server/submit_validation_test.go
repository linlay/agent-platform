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
		{name: "approval rule decision", mode: "approval", params: []map[string]any{{"decision": "approve_rule_run"}}},
		{name: "form approve", mode: "form", params: []map[string]any{{"decision": "approve", "form": map[string]any{"days": 2}}}},
		{name: "form reject", mode: "form", params: []map[string]any{{"decision": "reject"}}},
		{name: "form reject with reason", mode: "form", params: []map[string]any{{"decision": "reject", "reason": "不同意"}}},
		{name: "form reject with form", mode: "form", params: []map[string]any{{"decision": "reject", "reason": "已修改", "form": map[string]any{"days": 1}}}},
		{name: "plan dismiss", mode: "plan", params: []map[string]any{}},
		{name: "plan approve", mode: "plan", params: []map[string]any{{"decision": "approve"}}},
		{name: "plan reject empty reason", mode: "plan", params: []map[string]any{{"decision": "reject", "reason": ""}}},
		{name: "plan reject reason", mode: "plan", params: []map[string]any{{"decision": "reject", "reason": "请补充测试范围"}}},
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

func TestValidateDeferredSubmitParamsRejectsInvalidApprovalDecision(t *testing.T) {
	legacyDecision := "approve_" + "prefix_run"
	err := validateDeferredSubmitParams("approval", mustEncodeSubmitParams(t, []map[string]any{{"decision": legacyDecision}}))
	if err == nil || !strings.Contains(err.Error(), `items[0]: unsupported approval decision "`+legacyDecision+`"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateDeferredSubmitParamsRejectsInvalidPlanShape(t *testing.T) {
	tests := []struct {
		name       string
		params     any
		wantSubstr string
	}{
		{
			name:       "too many items",
			params:     []map[string]any{{"decision": "approve"}, {"decision": "reject"}},
			wantSubstr: "expected 1 submit items, got 2",
		},
		{
			name:       "invalid decision",
			params:     []map[string]any{{"decision": "approve_rule_run"}},
			wantSubstr: `items[0]: unsupported plan decision "approve_rule_run"`,
		},
		{
			name:       "answer rejected",
			params:     []map[string]any{{"decision": "reject", "answer": "no"}},
			wantSubstr: "items[0]: plan items do not allow answer",
		},
		{
			name:       "payload rejected",
			params:     []map[string]any{{"decision": "reject", "payload": map[string]any{}}},
			wantSubstr: "items[0]: plan items do not allow payload",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeferredSubmitParams("plan", mustEncodeSubmitParams(t, tt.params))
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("unexpected error: %v", err)
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
			name:       "missing decision",
			params:     []map[string]any{{"form": map[string]any{"days": 2}}},
			wantSubstr: "items[0]: form items require decision",
		},
		{
			name:       "invalid decision",
			params:     []map[string]any{{"decision": "cancel", "form": map[string]any{"days": 2}}},
			wantSubstr: `items[0]: unsupported form decision "cancel"`,
		},
		{
			name:       "approve missing form",
			params:     []map[string]any{{"decision": "approve"}},
			wantSubstr: "items[0]: approve decision requires form",
		},
		{
			name:       "form not object",
			params:     []map[string]any{{"decision": "approve", "form": "bad"}},
			wantSubstr: "items[0]: form field must be an object",
		},
		{
			name:       "action field rejected",
			params:     []map[string]any{{"action": "submit", "form": map[string]any{"days": 2}}},
			wantSubstr: "items[0]: form items no longer use action, use decision instead",
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
