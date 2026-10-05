package tools

import (
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
  - viewportType: html
    viewportKey: default_review
  - when:
      /args/type: remove
      /enabled: true
    viewportType: html
    viewportKey: deletion
  - when:
      /action: apply
    viewportType: html
    viewportKey: change
  - when:
      /items/0/a~1b~0c: 3
    viewportType: html
    viewportKey: escaped
`)
	for _, tc := range []struct {
		args map[string]any
		key  string
		bad  bool
	}{
		{map[string]any{"args": map[string]any{"type": "remove"}, "enabled": true}, "deletion", false},
		{map[string]any{"args": map[string]any{"type": "remove"}, "enabled": "true"}, "default_review", false},
		{map[string]any{"action": "apply"}, "change", false},
		{map[string]any{"items": []any{map[string]any{"a/b~c": float64(3)}}}, "escaped", false},
		{map[string]any{}, "default_review", false},
		{map[string]any{"action": "apply", "args": map[string]any{"type": "remove"}, "enabled": true}, "", true},
	} {
		got, err := selectConfirmationRule(raw, tc.args)
		if tc.bad {
			if err == nil {
				t.Fatal("ambiguous match accepted")
			}
			continue
		}
		if err != nil || got == nil || got.viewportKey != tc.key {
			t.Fatalf("%#v: got %#v, %v", tc.args, got, err)
		}
	}
	conditional := []any{map[string]any{"when": map[string]any{"/value": nil}, "viewportType": "html", "viewportKey": "null_review"}}
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
		[]any{map[string]any{"viewportType": "html", "viewportKey": "x", "when": map[string]any{}}},
		[]any{map[string]any{"viewportType": "html", "viewportKey": "x", "when": map[string]any{"action": "apply"}}},
		[]any{map[string]any{"viewportType": "html", "viewportKey": "x", "when": map[string]any{"/bad~2": "apply"}}},
		[]any{map[string]any{"viewportType": "html", "viewportKey": "../x"}},
		[]any{map[string]any{"viewportType": "builtin", "viewportKey": "x"}},
		[]any{map[string]any{"viewportType": "html", "viewportKey": "x", "typo": true}},
		[]any{map[string]any{"viewportType": "html", "viewportKey": "x"}, map[string]any{"viewportType": "html", "viewportKey": "y"}},
	} {
		if _, err := parseToolDefinition(map[string]any{"name": "test", "confirmationRules": raw}, toolDefinitionParseOptions{}); err == nil {
			t.Fatalf("accepted invalid config: %#v", raw)
		}
	}
}

type configuredApprovalHandler struct{ captureNamedToolHandler }

func (*configuredApprovalHandler) PrepareToolApproval(context.Context, string, map[string]any, *ExecutionContext) (*ToolApproval, error) {
	return &ToolApproval{Fingerprint: "frozen", ViewportKey: "handler_must_not_select", Form: map[string]any{"before": "old", "after": "new"}}, nil
}

func TestToolApprovalUsesOnlyConfiguredPresentation(t *testing.T) {
	for _, key := range []string{"custom_template", ""} {
		meta := map[string]any{"viewportType": "html", "viewportKey": "top_level_must_not_select"}
		if key != "" {
			meta["confirmationRules"] = []any{map[string]any{"viewportType": "html", "viewportKey": key}}
		}
		router := mustNewToolRouter(t, stubBackendToolExecutor{defs: []api.ToolDetailResponse{{Name: "example", Meta: meta}}}, nil, nil, nil)
		if err := router.RegisterHandler(&configuredApprovalHandler{captureNamedToolHandler{names: []string{"example"}}}); err != nil {
			t.Fatal(err)
		}
		plan, err := router.PrepareToolApproval(context.Background(), "example", map[string]any{}, nil)
		if err != nil || plan == nil || plan.ViewportKey != key || plan.Fingerprint != "frozen" || plan.Form["after"] != "new" {
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
			if wants && (got == nil || got.viewportKey != wantKey) {
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
