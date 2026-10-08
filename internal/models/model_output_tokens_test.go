package models

import (
	"strings"
	"testing"
)

func TestLoadModelRegistryParsesMaxOutputTokens(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: test", "protocol: ANTHROPIC", "maxOutputTokens: 128000")
	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := registry.Get("mock-model")
	if err != nil {
		t.Fatal(err)
	}
	if model.MaxOutputTokens != 128000 {
		t.Fatalf("maxOutputTokens = %d, want 128000", model.MaxOutputTokens)
	}
	listed := registry.List()
	if len(listed) != 1 || listed[0].MaxOutputTokens != 128000 {
		t.Fatalf("list lost output limit: %#v", listed)
	}
}

func TestLoadModelRegistryRejectsInvalidMaxOutputTokens(t *testing.T) {
	for _, value := range []string{"0", "-1", "1.5", "null", "true", "unlimited", "[]"} {
		t.Run(value, func(t *testing.T) {
			root := t.TempDir()
			writeTestProviderAndModel(t, root, "apiKey: test", "protocol: ANTHROPIC", "maxOutputTokens: "+value)
			if _, err := LoadModelRegistry(root); err == nil || !strings.Contains(err.Error(), "maxOutputTokens must be a positive integer") {
				t.Fatalf("expected invalid output limit to fail, got %v", err)
			}
		})
	}
}
