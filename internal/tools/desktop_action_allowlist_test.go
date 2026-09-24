package tools

import (
	"strings"
	"testing"
)

func TestDesktopActionSchemaDescribesDomainsWithoutActionEnum(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		if def.Name != "desktop_action" {
			continue
		}
		properties, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatal("missing properties")
		}
		action, ok := properties["action"].(map[string]any)
		if !ok || action["type"] != "string" {
			t.Fatal("action must be a string")
		}
		if _, present := action["enum"]; present {
			t.Fatal("runtime allowlist must not be sent as a model-facing enum")
		}
		description, _ := action["description"].(string)
		if !strings.Contains(description, "desktop-action skill") || !strings.Contains(description, "never a wildcard") {
			t.Fatal("action schema must direct the model to exact names in the skill")
		}
		allowed, err := loadDesktopActionAllowlist()
		if err != nil {
			t.Fatal(err)
		}
		for name := range allowed {
			parts := strings.Split(name, ".")
			scope := name
			if len(parts) > 2 {
				scope = strings.Join(parts[:2], ".") + ".*"
			}
			if !strings.Contains(description, scope) {
				t.Errorf("missing scope %s", scope)
			}
			if len(parts) > 2 && strings.Contains(description, name) {
				t.Errorf("exact action leaked back into schema: %s", name)
			}
		}
		return
	}
	t.Fatal("desktop_action tool definition missing")
}

func TestDesktopActionRuntimePolicyRejectsInvalidDefinitions(t *testing.T) {
	for _, names := range [][]string{
		nil, {"desktop.skin.*"}, {"desktop..get"}, {"desktop.skin.get "},
		{"other.skin.get"}, {"desktop.skin.get", "desktop.skin.get"},
	} {
		if _, err := buildDesktopActionAllowlist(names); err == nil {
			t.Errorf("accepted malformed policy %v", names)
		}
	}
	allowed, err := buildDesktopActionAllowlist([]string{"desktop.skin.get", "desktop.display"})
	if err != nil || !allowed["desktop.skin.get"] || !allowed["desktop.display"] || allowed["desktop.skin.futureAction"] {
		t.Fatalf("exact allowlist = %v, error = %v", allowed, err)
	}
}
