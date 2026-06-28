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

func TestNormalizeFormUsesExplicitDecisionAndForm(t *testing.T) {
	args := map[string]any{
		"forms": []any{
			map[string]any{
				"id":      "form-1",
				"command": "mock create-leave --payload '{}'",
			},
		},
	}

	normalized, err := NormalizeForm(args, []any{
		map[string]any{
			"id":       "form-1",
			"decision": "approve",
			"form": map[string]any{
				"days": 2,
			},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeForm returned error: %v", err)
	}

	forms, _ := normalized["forms"].([]map[string]any)
	if len(forms) != 1 {
		t.Fatalf("expected one normalized form, got %#v", normalized)
	}
	form, _ := forms[0]["form"].(map[string]any)
	if forms[0]["decision"] != "approve" || form["days"] != 2 {
		t.Fatalf("unexpected normalized form %#v", forms[0])
	}
}

func TestNormalizeFormHandlesRejectAndDismiss(t *testing.T) {
	args := map[string]any{
		"forms": []any{
			map[string]any{"id": "form-1", "command": "cmd-1"},
			map[string]any{"id": "form-2", "command": "cmd-2"},
		},
	}

	normalized, err := NormalizeForm(args, []any{
		map[string]any{"id": "form-1", "decision": "reject"},
		map[string]any{"id": "form-2", "decision": "reject", "reason": "不同意", "form": map[string]any{"days": 1}},
	})
	if err != nil {
		t.Fatalf("NormalizeForm returned error: %v", err)
	}

	forms, _ := normalized["forms"].([]map[string]any)
	if len(forms) != 2 {
		t.Fatalf("expected two normalized forms, got %#v", normalized)
	}
	revisedForm, _ := forms[1]["form"].(map[string]any)
	if forms[0]["decision"] != "reject" || forms[1]["decision"] != "reject" || forms[1]["reason"] != "不同意" || revisedForm["days"] != 1 {
		t.Fatalf("unexpected normalized decisions %#v", forms)
	}
	if _, ok := forms[0]["form"]; ok {
		t.Fatalf("did not expect reject to retain form data, got %#v", forms[0])
	}

	dismissed, err := NormalizeForm(args, []any{})
	if err != nil {
		t.Fatalf("NormalizeForm dismiss returned error: %v", err)
	}
	if dismissed["status"] != "error" {
		t.Fatalf("expected dismissed status=error, got %#v", dismissed)
	}
}

func TestNormalizeFormRejectsMissingDecisionOrForm(t *testing.T) {
	args := map[string]any{
		"forms": []any{
			map[string]any{"id": "form-1", "command": "cmd-1"},
		},
	}

	tests := []struct {
		name string
		item map[string]any
	}{
		{name: "missing decision", item: map[string]any{"id": "form-1"}},
		{name: "approve missing form", item: map[string]any{"id": "form-1", "decision": "approve"}},
		{name: "invalid decision", item: map[string]any{"id": "form-1", "decision": "cancel"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NormalizeForm(args, []any{tt.item}); err == nil {
				t.Fatalf("expected error for %#v", tt.item)
			}
		})
	}
}

func TestNormalizeFormOmitsEmptyReason(t *testing.T) {
	args := map[string]any{
		"forms": []any{
			map[string]any{"id": "form-1", "command": "cmd-1"},
		},
	}

	normalized, err := NormalizeForm(args, []any{
		map[string]any{"id": "form-1", "decision": "reject", "reason": "  ", "form": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("NormalizeForm returned error: %v", err)
	}

	forms, _ := normalized["forms"].([]map[string]any)
	if len(forms) != 1 {
		t.Fatalf("expected one normalized form, got %#v", normalized)
	}
	if _, ok := forms[0]["reason"]; ok {
		t.Fatalf("did not expect empty reason to be retained, got %#v", forms[0])
	}
	if _, ok := forms[0]["form"]; ok {
		t.Fatalf("did not expect empty form to be retained, got %#v", forms[0])
	}
}
