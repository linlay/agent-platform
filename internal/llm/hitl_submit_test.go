package llm

import (
	"testing"

	"agent-platform/internal/api"
)

func mustEncodeHITLSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := api.EncodeSubmitParams(value)
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	return params
}

func TestNormalizePlanningConfirmationSubmitApprove(t *testing.T) {
	normalized, err := normalizePlanningConfirmationSubmit(map[string]any{
		"mode": "plan",
		"plan": map[string]any{"id": "confirm", "planningId": "run_1_planning_1"},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{
		{"id": "confirm", "decision": "approve"},
	}))
	if err != nil {
		t.Fatalf("normalizePlanningConfirmationSubmit returned error: %v", err)
	}
	plan, _ := normalized["plan"].(map[string]any)
	if normalized["mode"] != "plan" || normalized["status"] != "answered" {
		t.Fatalf("unexpected normalized plan submit %#v", normalized)
	}
	if plan["id"] != "confirm" || plan["planningId"] != "run_1_planning_1" || plan["decision"] != "approve" {
		t.Fatalf("unexpected normalized plan %#v", normalized)
	}
}

func TestNormalizePlanningConfirmationSubmitRejectPreservesReason(t *testing.T) {
	normalized, err := normalizePlanningConfirmationSubmit(map[string]any{
		"mode": "plan",
		"plan": map[string]any{"id": "confirm", "planningId": "run_1_planning_1"},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{
		{"id": "confirm", "decision": "reject", "reason": " 请补充测试范围 "},
	}))
	if err != nil {
		t.Fatalf("normalizePlanningConfirmationSubmit returned error: %v", err)
	}
	plan, _ := normalized["plan"].(map[string]any)
	if plan["decision"] != "reject" || plan["reason"] != "请补充测试范围" {
		t.Fatalf("expected reject reason to be preserved, got %#v", normalized)
	}
}

func TestNormalizePlanningConfirmationSubmitDismiss(t *testing.T) {
	normalized, err := normalizePlanningConfirmationSubmit(map[string]any{
		"mode": "plan",
		"plan": map[string]any{"id": "confirm", "planningId": "run_1_planning_1"},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{}))
	if err != nil {
		t.Fatalf("normalizePlanningConfirmationSubmit returned error: %v", err)
	}
	errPayload, _ := normalized["error"].(map[string]any)
	if normalized["mode"] != "plan" || normalized["status"] != "error" || errPayload["code"] != "user_dismissed" {
		t.Fatalf("unexpected dismiss normalization %#v", normalized)
	}
}

func TestNormalizePlanningConfirmationSubmitRejectsUnknownDecision(t *testing.T) {
	_, err := normalizePlanningConfirmationSubmit(map[string]any{
		"mode": "plan",
		"plan": map[string]any{"id": "confirm", "planningId": "run_1_planning_1"},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{
		{"id": "confirm", "decision": "approve_rule_run"},
	}))
	if err == nil || err.Error() != `items[0]: unsupported planning confirmation decision "approve_rule_run"` {
		t.Fatalf("expected unsupported decision error, got %v", err)
	}
}
