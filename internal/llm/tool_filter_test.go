package llm

import (
	"testing"

	"agent-platform/internal/api"
)

func TestFilterToolDefinitionsSkipsExplicitOnlyWhenAllowedToolsEmpty(t *testing.T) {
	defs := []api.ToolDetailResponse{
		{Name: "datetime"},
		{Name: "vision_recognize", Meta: map[string]any{"explicitOnly": true}},
	}

	filtered := filterToolDefinitions(defs, nil)
	if len(filtered) != 1 || filtered[0].Name != "datetime" {
		t.Fatalf("expected only non-explicit tool, got %#v", filtered)
	}

	filtered = filterToolDefinitions(defs, []string{"vision_recognize"})
	if len(filtered) != 1 || filtered[0].Name != "vision_recognize" {
		t.Fatalf("expected explicit tool when allowed by name, got %#v", filtered)
	}
}
