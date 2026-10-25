package tools

import (
	"strings"
	"testing"

	"agent-platform/internal/contracts"
)

func TestParseToolActivityArgs(t *testing.T) {
	schema := map[string]any{"properties": map[string]any{
		"description": map[string]any{"type": "string"},
		"filePath":    map[string]any{"type": "string"},
		"limit":       map[string]any{"type": "integer"},
	}}
	def, err := parseToolDefinition(map[string]any{
		"name": "file_write", "inputSchema": schema, "activityArgs": []any{"description", "filePath"},
	}, toolDefinitionParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := contracts.ToolActivityArgs(def.Meta); len(got) != 2 || got[0] != "description" || got[1] != "filePath" {
		t.Fatalf("activityArgs=%#v", got)
	}
	for raw, want := range map[string]any{
		"not a list":     "description",
		"missing arg":    []any{"path"},
		"non-string arg": []any{"limit"},
		"blank name":     []any{""},
	} {
		_, err := parseToolDefinition(map[string]any{"name": "x", "inputSchema": schema, "activityArgs": want}, toolDefinitionParseOptions{})
		if err == nil || !strings.Contains(err.Error(), "activityArgs") {
			t.Fatalf("%s: err=%v", raw, err)
		}
	}
}

func TestEmbeddedToolActivityArgsNameStringInputs(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	configured := map[string]bool{}
	for _, def := range defs {
		if len(contracts.ToolActivityArgs(def.Meta)) > 0 {
			configured[def.Name] = true
		}
	}
	for _, name := range []string{"bash", "file_read", "file_write", "file_edit", "web_fetch", "wait"} {
		if !configured[name] {
			t.Fatalf("%s has no activityArgs", name)
		}
	}
}
