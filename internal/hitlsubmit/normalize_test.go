package hitlsubmit

import "testing"

func TestNormalizeFormUsesExplicitActionAndForm(t *testing.T) {
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
			"id":     "form-1",
			"action": "submit",
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
	if forms[0]["action"] != "submit" || form["days"] != 2 {
		t.Fatalf("unexpected normalized form %#v", forms[0])
	}
}

func TestNormalizeFormHandlesRejectCancelAndDismiss(t *testing.T) {
	args := map[string]any{
		"forms": []any{
			map[string]any{"id": "form-1", "command": "cmd-1"},
			map[string]any{"id": "form-2", "command": "cmd-2"},
		},
	}

	normalized, err := NormalizeForm(args, []any{
		map[string]any{"id": "form-1", "action": "reject"},
		map[string]any{"id": "form-2", "action": "cancel"},
	})
	if err != nil {
		t.Fatalf("NormalizeForm returned error: %v", err)
	}

	forms, _ := normalized["forms"].([]map[string]any)
	if len(forms) != 2 {
		t.Fatalf("expected two normalized forms, got %#v", normalized)
	}
	if forms[0]["action"] != "reject" || forms[1]["action"] != "cancel" {
		t.Fatalf("unexpected normalized actions %#v", forms)
	}
	if _, ok := forms[0]["form"]; ok {
		t.Fatalf("did not expect reject to retain form data, got %#v", forms[0])
	}
	if _, ok := forms[1]["form"]; ok {
		t.Fatalf("did not expect cancel to retain form data, got %#v", forms[1])
	}

	dismissed, err := NormalizeForm(args, []any{})
	if err != nil {
		t.Fatalf("NormalizeForm dismiss returned error: %v", err)
	}
	if dismissed["status"] != "error" {
		t.Fatalf("expected dismissed status=error, got %#v", dismissed)
	}
}

func TestNormalizeFormRejectsMissingActionOrForm(t *testing.T) {
	args := map[string]any{
		"forms": []any{
			map[string]any{"id": "form-1", "command": "cmd-1"},
		},
	}

	tests := []struct {
		name string
		item map[string]any
	}{
		{name: "missing action", item: map[string]any{"id": "form-1"}},
		{name: "submit missing form", item: map[string]any{"id": "form-1", "action": "submit"}},
		{name: "invalid action", item: map[string]any{"id": "form-1", "action": "approve"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NormalizeForm(args, []any{tt.item}); err == nil {
				t.Fatalf("expected error for %#v", tt.item)
			}
		})
	}
}
