package hitl

import "testing"

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
