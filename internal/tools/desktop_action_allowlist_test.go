package tools

import (
	"agent-platform/internal/connector"
	"strings"
	"testing"
)

func TestDesktopDomainSchemasMatchRegistry(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, def := range defs {
		if !strings.HasPrefix(def.Name, "desktop_") {
			continue
		}
		count++
		props := def.Parameters["properties"].(map[string]any)
		enum := props["action"].(map[string]any)["enum"].([]any)
		for _, v := range enum {
			if _, ok := connector.LookupControlAction(def.Name, v.(string)); !ok {
				t.Fatalf("unregistered action %s %v", def.Name, v)
			}
		}
	}
	if count != 7 {
		t.Fatalf("desktop tool count %d", count)
	}
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
