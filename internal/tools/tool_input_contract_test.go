package tools

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestEmbeddedInputPropertiesAreCamelCase(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	namePattern := regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)
	// Opaque protocol and user-owned dictionaries are not platform field schemas.
	opaque := map[string]bool{"desktop_action.args": true, "desktop_cdp.params": true, "platform_control.params": true, "run_env.params": true}
	var walk func(string, any)
	walk = func(path string, value any) {
		switch node := value.(type) {
		case map[string]any:
			if props, ok := node["properties"].(map[string]any); ok {
				for name, child := range props {
					if !namePattern.MatchString(name) {
						t.Errorf("non-camelCase input property %s.%s", path, name)
					}
					childPath := path + "." + name
					if !opaque[childPath] {
						walk(childPath, child)
					}
				}
			}
			for key, child := range node {
				if key != "properties" {
					walk(path+"."+key, child)
				}
			}
		case []any:
			for _, child := range node {
				walk(path, child)
			}
		}
	}
	for _, def := range defs {
		walk(def.Name, def.Parameters)
	}
}

func TestToolDefinitionRejectsLegacyParametersEvenWithInputSchema(t *testing.T) {
	for _, both := range []bool{false, true} {
		root := map[string]any{"name": "demo", "parameters": map[string]any{"type": "object"}}
		if both {
			root["inputSchema"] = map[string]any{"type": "object"}
		}
		_, err := parseToolDefinition(root, toolDefinitionParseOptions{sourceType: "agent-local"})
		if err == nil || !strings.Contains(err.Error(), "use inputSchema") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestRouterRejectsLegacyBeforeBackend(t *testing.T) {
	router := mustNewToolRouter(t, stubBackendToolExecutor{defs: []api.ToolDetailResponse{{Name: "file_write"}, {Name: "kbase_files"}}}, nil, nil, nil)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"file_write", map[string]any{"filePath": "new", "file_path": "old", "content": "must not write"}},
		{"kbase_files", map[string]any{"head_limit": 0}},
	} {
		result, err := router.Invoke(context.Background(), tc.tool, tc.args, nil)
		if err != nil || result.Error != "invalid_tool_arguments" {
			t.Fatalf("unexpected result: %#v %v", result, err)
		}
	}
}

func TestRuntimeRejectsLegacyBeforeFileSideEffects(t *testing.T) {
	result, err := (&RuntimeToolExecutor{}).Invoke(context.Background(), "file_write", map[string]any{"file_path": "must-not-be-written", "content": "secret"}, &contracts.ExecutionContext{})
	if err != nil || result.Error != "invalid_tool_arguments" {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
}
