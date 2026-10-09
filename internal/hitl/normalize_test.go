package hitl

import (
	"testing"

	"agent-platform/internal/api"
)

func mustEncodeSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := api.EncodeSubmitParams(value)
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	return params
}

func TestNormalizeApprovalSupportsApproveRuleRunForCommand(t *testing.T) {
	normalized, err := NormalizeApproval(map[string]any{
		"approvals": []any{
			map[string]any{"id": "tool_1", "command": "chmod 777 ~/a.sh"},
		},
	}, mustEncodeSubmitParams(t, []map[string]any{
		{"id": "tool_1", "decision": "approve_rule_run", "reason": "同规则本轮一并放行"},
	}))
	if err != nil {
		t.Fatalf("NormalizeApproval returned error: %v", err)
	}

	approvals, ok := normalized["approvals"].([]map[string]any)
	if !ok || len(approvals) != 1 {
		t.Fatalf("expected one normalized approval, got %#v", normalized)
	}
	if normalized["status"] != "answered" {
		t.Fatalf("expected answered status, got %#v", normalized)
	}
	if approvals[0]["decision"] != "approve_rule_run" {
		t.Fatalf("expected approve_rule_run decision to be preserved, got %#v", approvals[0])
	}
	if approvals[0]["reason"] != "同规则本轮一并放行" {
		t.Fatalf("expected reason to be preserved, got %#v", approvals[0])
	}
}

func TestNormalizeApprovalSupportsApproveRuleRunForFile(t *testing.T) {
	normalized, err := NormalizeApproval(map[string]any{
		"approvals": []any{
			map[string]any{"id": "tool_1", "command": "file_read /tmp/owner.md"},
		},
	}, mustEncodeSubmitParams(t, []map[string]any{
		{"id": "tool_1", "decision": "approve_rule_run", "reason": "同规则本轮一并放行"},
	}))
	if err != nil {
		t.Fatalf("NormalizeApproval returned error: %v", err)
	}

	approvals, ok := normalized["approvals"].([]map[string]any)
	if !ok || len(approvals) != 1 {
		t.Fatalf("expected one normalized approval, got %#v", normalized)
	}
	if approvals[0]["decision"] != "approve_rule_run" {
		t.Fatalf("expected approve_rule_run decision to be preserved, got %#v", approvals[0])
	}
	if approvals[0]["reason"] != "同规则本轮一并放行" {
		t.Fatalf("expected reason to be preserved, got %#v", approvals[0])
	}
}

func TestNormalizeApprovalRejectsEmptyDecision(t *testing.T) {
	_, err := NormalizeApproval(map[string]any{
		"approvals": []any{
			map[string]any{"id": "tool_1", "command": "chmod 777 ~/a.sh"},
		},
	}, mustEncodeSubmitParams(t, []map[string]any{
		{"id": "tool_1", "decision": ""},
	}))
	if err == nil || err.Error() != "items[0]: decision is required" {
		t.Fatalf("expected empty decision error, got %v", err)
	}
}

func TestNormalizeApprovalRejectsUnknownDecision(t *testing.T) {
	_, err := NormalizeApproval(map[string]any{
		"approvals": []any{
			map[string]any{"id": "tool_1", "command": "chmod 777 ~/a.sh"},
		},
	}, mustEncodeSubmitParams(t, []map[string]any{
		{"id": "tool_1", "decision": "approve_always", "reason": "历史回放"},
	}))
	if err == nil || err.Error() != `items[0]: unsupported approval decision "approve_always"` {
		t.Fatalf("expected unsupported decision error, got %v", err)
	}
}

func TestNormalizeFormUsesDecisionAndData(t *testing.T) {
	args := map[string]any{"form": map[string]any{"command": "mock create-leave --payload '{}'"}}

	normalized, err := NormalizeForm(args, map[string]any{"decision": "approve", "data": map[string]any{"days": 2}})
	if err != nil {
		t.Fatalf("NormalizeForm returned error: %v", err)
	}
	form, _ := normalized["form"].(map[string]any)
	data, _ := form["data"].(map[string]any)
	if normalized["status"] != "answered" || form["decision"] != "approve" || data["days"] != 2 || form["command"] == "" {
		t.Fatalf("unexpected normalized form %#v", normalized)
	}
	if _, ok := form["id"]; ok {
		t.Fatalf("did not expect a form id, got %#v", form)
	}
}

func TestNormalizeFormHandlesRejectAndDismiss(t *testing.T) {
	args := map[string]any{"form": map[string]any{"command": "cmd-1"}}

	plain, err := NormalizeForm(args, map[string]any{"decision": "reject", "reason": "  ", "data": map[string]any{}})
	if err != nil {
		t.Fatalf("NormalizeForm returned error: %v", err)
	}
	form, _ := plain["form"].(map[string]any)
	if form["decision"] != "reject" {
		t.Fatalf("unexpected reject %#v", plain)
	}
	if _, ok := form["reason"]; ok {
		t.Fatalf("did not expect empty reason to be retained, got %#v", form)
	}
	if _, ok := form["data"]; ok {
		t.Fatalf("did not expect empty data to be retained, got %#v", form)
	}

	revised, err := NormalizeForm(args, map[string]any{"decision": "reject", "reason": "不同意", "data": map[string]any{"days": 1}})
	if err != nil {
		t.Fatalf("NormalizeForm returned error: %v", err)
	}
	form, _ = revised["form"].(map[string]any)
	data, _ := form["data"].(map[string]any)
	if form["reason"] != "不同意" || data["days"] != 1 {
		t.Fatalf("expected reject to keep the edited data, got %#v", revised)
	}

	dismissed, err := NormalizeForm(args, map[string]any{"decision": "dismiss"})
	if err != nil {
		t.Fatalf("NormalizeForm dismiss returned error: %v", err)
	}
	errPayload, _ := dismissed["error"].(map[string]any)
	if dismissed["status"] != "error" || errPayload["code"] != "user_dismissed" {
		t.Fatalf("expected user_dismissed, got %#v", dismissed)
	}
}

func TestNormalizeFormRejectsInvalidParam(t *testing.T) {
	args := map[string]any{"form": map[string]any{"command": "cmd-1"}}

	tests := []struct {
		name  string
		param any
	}{
		{name: "missing param", param: nil},
		{name: "empty param", param: map[string]any{}},
		{name: "item list", param: []any{map[string]any{"decision": "approve", "data": map[string]any{}}}},
		{name: "missing decision", param: map[string]any{"data": map[string]any{}}},
		{name: "approve missing data", param: map[string]any{"decision": "approve"}},
		{name: "data not an object", param: map[string]any{"decision": "approve", "data": "x"}},
		{name: "invalid decision", param: map[string]any{"decision": "cancel"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NormalizeForm(args, tt.param); err == nil {
				t.Fatalf("expected error for %#v", tt.param)
			}
		})
	}
}
