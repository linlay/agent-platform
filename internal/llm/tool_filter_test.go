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

func TestFilterToolDefinitionsRequiresExplicitPlatformConfigGrant(t *testing.T) {
	defs := []api.ToolDetailResponse{
		{Name: "datetime"},
		{Name: "platform_config", Meta: map[string]any{"explicitOnly": true}},
	}

	if filtered := filterToolDefinitions(defs, nil); len(filtered) != 1 || filtered[0].Name != "datetime" {
		t.Fatalf("platform_config must be hidden without an explicit tool list, got %#v", filtered)
	}
	if filtered := filterToolDefinitions(defs, []string{"datetime"}); len(filtered) != 1 || filtered[0].Name != "datetime" {
		t.Fatalf("platform_config must be hidden from unrelated explicit grants, got %#v", filtered)
	}
	if filtered := filterToolDefinitions(defs, []string{"platform_config"}); len(filtered) != 1 || filtered[0].Name != "platform_config" {
		t.Fatalf("platform_config explicit grant was not honored, got %#v", filtered)
	}
}

func TestEffectiveToolDefinitionsUseSandboxBashSchema(t *testing.T) {
	defs := []api.ToolDetailResponse{
		{
			Key:         "bash",
			Name:        "bash",
			Description: "host bash",
			Parameters:  map[string]any{"properties": map[string]any{"command": map[string]any{}}},
		},
		{
			Key:         "bash_sandbox",
			Name:        "bash_sandbox",
			Description: "sandbox bash",
			Parameters:  map[string]any{"properties": map[string]any{"command": map[string]any{}, "description": map[string]any{}}},
		},
	}

	hostDefs := effectiveToolDefinitions(defs, []string{"bash"}, false)
	if len(hostDefs) != 1 || hostDefs[0].Name != "bash" || hostDefs[0].Description != "host bash" {
		t.Fatalf("expected host bash definition, got %#v", hostDefs)
	}

	sandboxDefs := effectiveToolDefinitions(defs, []string{"bash"}, true)
	if len(sandboxDefs) != 1 {
		t.Fatalf("expected one sandbox bash definition, got %#v", sandboxDefs)
	}
	if sandboxDefs[0].Name != "bash" || sandboxDefs[0].Key != "bash" {
		t.Fatalf("expected sandbox schema to remain exposed as bash, got %#v", sandboxDefs[0])
	}
	if sandboxDefs[0].Description != "sandbox bash" {
		t.Fatalf("expected sandbox bash description, got %#v", sandboxDefs[0])
	}
	properties, _ := sandboxDefs[0].Parameters["properties"].(map[string]any)
	if _, ok := properties["description"]; !ok {
		t.Fatalf("expected sandbox bash parameters to include description, got %#v", sandboxDefs[0].Parameters)
	}

	allSandboxDefs := effectiveToolDefinitions(defs, nil, true)
	if len(allSandboxDefs) != 1 || allSandboxDefs[0].Name != "bash" {
		t.Fatalf("expected internal bash_sandbox to be hidden from sandbox tool list, got %#v", allSandboxDefs)
	}
}
