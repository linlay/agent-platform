package tools

import (
	"agent-platform/internal/view"
	"context"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func confirmationYAML(t *testing.T, source string) any {
	t.Helper()
	root, err := config.LoadYAMLTreeBytes([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	return root.(map[string]any)["confirmationRules"]
}

func TestConfirmationRuleSelection(t *testing.T) {
	raw := confirmationYAML(t, `confirmationRules:
  - view: {key: platform_control_review}
  - when:
      /args/type: remove
      /enabled: true
    view: {key: resource_delete_review}
  - when:
      /action: apply
    view: {key: installation_review}
  - when:
      /items/0/a~1b~0c: 3
    view: {key: chat_delete_review}
`)
	for _, tc := range []struct {
		args map[string]any
		key  string
		bad  bool
	}{
		{map[string]any{"args": map[string]any{"type": "remove"}, "enabled": true}, "resource_delete_review", false},
		{map[string]any{"args": map[string]any{"type": "remove"}, "enabled": "true"}, "platform_control_review", false},
		{map[string]any{"action": "apply"}, "installation_review", false},
		{map[string]any{"items": []any{map[string]any{"a/b~c": float64(3)}}}, "chat_delete_review", false},
		{map[string]any{}, "platform_control_review", false},
		{map[string]any{"action": "apply", "args": map[string]any{"type": "remove"}, "enabled": true}, "", true},
	} {
		got, err := selectConfirmationRule(raw, tc.args)
		if tc.bad {
			if err == nil {
				t.Fatal("ambiguous match accepted")
			}
			continue
		}
		if err != nil || got == nil || got.view.Key != tc.key {
			t.Fatalf("%#v: got %#v, %v", tc.args, got, err)
		}
	}
	conditional := []any{map[string]any{"when": map[string]any{"/value": nil}, "view": map[string]any{"key": "platform_control_review"}}}
	got, err := selectConfirmationRule(conditional, map[string]any{})
	if err != nil || got != nil {
		t.Fatal("missing must not equal null")
	}
	got, err = selectConfirmationRule(conditional, map[string]any{"value": nil})
	if err != nil || got == nil {
		t.Fatal("explicit null should match")
	}
}

func TestConfirmationRulesDefinitionValidation(t *testing.T) {
	for _, raw := range []any{
		nil, "bad", []any{"bad"},
		[]any{map[string]any{"view": map[string]any{"key": "x"}, "when": map[string]any{}}},
		[]any{map[string]any{"view": map[string]any{"key": "x"}, "when": map[string]any{"action": "apply"}}},
		[]any{map[string]any{"view": map[string]any{"key": "x"}, "when": map[string]any{"/bad~2": "apply"}}},
		[]any{map[string]any{"view": map[string]any{"key": "../x"}}},
		[]any{map[string]any{"view": map[string]any{"key": "x"}}},
		[]any{map[string]any{"view": map[string]any{"key": "x"}, "typo": true}},
		[]any{map[string]any{"view": map[string]any{"key": "x"}}, map[string]any{"view": map[string]any{"key": "y"}}},
	} {
		if _, err := parseToolDefinition(map[string]any{"name": "test", "confirmationRules": raw}, toolDefinitionParseOptions{}); err == nil {
			t.Fatalf("accepted invalid config: %#v", raw)
		}
	}
}

type configuredApprovalHandler struct{ captureNamedToolHandler }

func (*configuredApprovalHandler) PrepareToolApproval(context.Context, string, map[string]any, *ExecutionContext) (*ToolApproval, error) {
	return &ToolApproval{Fingerprint: "frozen", View: view.Builtin("installation_review"), Form: map[string]any{"before": "old", "after": "new"}}, nil
}

func TestToolApprovalUsesOnlyConfiguredPresentation(t *testing.T) {
	for _, key := range []string{"platform_control_review", ""} {
		meta := map[string]any{"view": map[string]any{"key": "installation_review"}}
		if key != "" {
			meta["confirmationRules"] = []any{map[string]any{"view": map[string]any{"key": key}}}
		}
		router := mustNewToolRouter(t, stubBackendToolExecutor{defs: []api.ToolDetailResponse{{Name: "example", Meta: meta}}}, nil, nil, nil)
		if err := router.RegisterHandler(&configuredApprovalHandler{captureNamedToolHandler{names: []string{"example"}}}); err != nil {
			t.Fatal(err)
		}
		plan, err := router.PrepareToolApproval(context.Background(), "example", map[string]any{}, nil)
		if err != nil || plan == nil || ((plan.View == nil) != (key == "")) || (plan.View != nil && plan.View.Key != key) || plan.Fingerprint != "frozen" || plan.Form["after"] != "new" {
			t.Fatalf("got %#v %v", plan, err)
		}
	}
}

func TestEmbeddedCatalogAndChatConfirmationRules(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, def := range defs {
		if def.Name != "catalog_manage" && def.Name != "chat_manage" {
			continue
		}
		found++
		for _, action := range []string{"apply", "delete", "rename"} {
			got, err := selectConfirmationRule(def.Meta["confirmationRules"], map[string]any{"action": action})
			if err != nil {
				t.Fatal(err)
			}
			wants := def.Name == "catalog_manage" || action == "delete"
			wantKey := "platform_control_review"
			if def.Name == "chat_manage" {
				wantKey = "chat_delete_review"
			}
			if def.Name == "catalog_manage" && action == "delete" {
				wantKey = "resource_delete_review"
			}
			if wants && (got == nil || got.view.Key != wantKey) {
				t.Fatalf("missing %s %s", def.Name, action)
			}
			if !wants && got != nil {
				t.Fatalf("unexpected %s %s", def.Name, action)
			}
		}
	}
	if found != 2 {
		t.Fatal("missing embedded tools")
	}
}

func TestEmbeddedAutomationConfirmationRules(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		if def.Name != "automation_manage" {
			continue
		}
		for action, key := range map[string]string{"create": "automation_review", "update": "automation_review", "setEnabled": "automation_review", "trigger": "automation_trigger_review", "delete": "automation_delete_review"} {
			got, err := selectConfirmationRule(def.Meta["confirmationRules"], map[string]any{"action": action})
			if err != nil || got == nil || got.view.Key != key {
				t.Fatalf("%s: %+v %v", action, got, err)
			}
		}
		return
	}
	t.Fatal("automation_manage missing")
}

// chat_start has one review page for every request the handler sends to review.
func TestEmbeddedChatStartConfirmationRule(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		if def.Name != "chat_start" {
			continue
		}
		for _, args := range []map[string]any{{"accessLevel": "full_access"}, {"accessLevel": "auto_approve"}, {}} {
			got, err := selectConfirmationRule(def.Meta["confirmationRules"], args)
			if err != nil || got == nil || got.view.Key != "chat_start_review" {
				t.Fatalf("args=%v rule=%#v err=%v", args, got, err)
			}
		}
		return
	}
	t.Fatal("missing chat_start")
}
