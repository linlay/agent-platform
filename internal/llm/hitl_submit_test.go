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

func TestNormalizeHITLPlanSubmitApprove(t *testing.T) {
	normalized, err := normalizeHITLPlanSubmit(map[string]any{
		"mode": "plan",
		"plan": map[string]any{"id": "confirm", "planningId": "run_1_planning_1"},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{
		{"id": "confirm", "decision": "approve"},
	}))
	if err != nil {
		t.Fatalf("normalizeHITLPlanSubmit returned error: %v", err)
	}
	plan, _ := normalized["plan"].(map[string]any)
	if normalized["mode"] != "plan" || normalized["status"] != "answered" {
		t.Fatalf("unexpected normalized plan submit %#v", normalized)
	}
	if plan["id"] != "confirm" || plan["planningId"] != "run_1_planning_1" || plan["decision"] != "approve" {
		t.Fatalf("unexpected normalized plan %#v", normalized)
	}
}

func TestNormalizeHITLPlanSubmitRejectPreservesReason(t *testing.T) {
	normalized, err := normalizeHITLPlanSubmit(map[string]any{
		"mode": "plan",
		"plan": map[string]any{"id": "confirm", "planningId": "run_1_planning_1"},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{
		{"id": "confirm", "decision": "reject", "reason": " 请补充测试范围 "},
	}))
	if err != nil {
		t.Fatalf("normalizeHITLPlanSubmit returned error: %v", err)
	}
	plan, _ := normalized["plan"].(map[string]any)
	if plan["decision"] != "reject" || plan["reason"] != "请补充测试范围" {
		t.Fatalf("expected reject reason to be preserved, got %#v", normalized)
	}
}

func TestNormalizeHITLPlanSubmitDismiss(t *testing.T) {
	normalized, err := normalizeHITLPlanSubmit(map[string]any{
		"mode": "plan",
		"plan": map[string]any{"id": "confirm", "planningId": "run_1_planning_1"},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{}))
	if err != nil {
		t.Fatalf("normalizeHITLPlanSubmit returned error: %v", err)
	}
	errPayload, _ := normalized["error"].(map[string]any)
	if normalized["mode"] != "plan" || normalized["status"] != "error" || errPayload["code"] != "user_dismissed" {
		t.Fatalf("unexpected dismiss normalization %#v", normalized)
	}
}

func TestNormalizeHITLPlanSubmitRejectsUnknownDecision(t *testing.T) {
	_, err := normalizeHITLPlanSubmit(map[string]any{
		"mode": "plan",
		"plan": map[string]any{"id": "confirm", "planningId": "run_1_planning_1"},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{
		{"id": "confirm", "decision": "approve_rule_run"},
	}))
	if err == nil || err.Error() != `items[0]: unsupported plan decision "approve_rule_run"` {
		t.Fatalf("expected unsupported decision error, got %v", err)
	}
}
