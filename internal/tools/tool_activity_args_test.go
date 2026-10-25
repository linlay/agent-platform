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

func TestParseToolActivityAnnounce(t *testing.T) {
	schema := map[string]any{"properties": map[string]any{
		"message": map[string]any{"type": "string"},
		"limit":   map[string]any{"type": "integer"},
	}}
	for raw, value := range map[string]any{"missing arg": "text", "non-string arg": "limit", "not a name": []any{"message"}, "blank": ""} {
		_, err := parseToolDefinition(map[string]any{"name": "x", "inputSchema": schema, "activityAnnounce": value}, toolDefinitionParseOptions{})
		if err == nil || !strings.Contains(err.Error(), "activityAnnounce") {
			t.Fatalf("%s: err=%v", raw, err)
		}
	}
	_, err := parseToolDefinition(map[string]any{"name": "x", "inputSchema": schema, "activityAnnounce": "message", "activityArgs": []any{"message"}}, toolDefinitionParseOptions{})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("activityAnnounce with activityArgs: err=%v", err)
	}
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		if def.Name == contracts.PlanAnnounceToolName {
			if contracts.ToolActivityAnnounce(def.Meta) != "message" {
				t.Fatalf("plan_announce must announce its message: %#v", def.Meta)
			}
			return
		}
	}
	t.Fatal("plan_announce is not embedded")
}

func TestPlanAnnounceValidatesMessage(t *testing.T) {
	if result := invokePlanAnnounce(map[string]any{"message": "Read the config."}); result.ExitCode != 0 {
		t.Fatalf("valid announcement failed: %#v", result)
	}
	for _, message := range []any{nil, "  ", strings.Repeat("长", planAnnounceMaxRunes+1)} {
		if result := invokePlanAnnounce(map[string]any{"message": message}); result.ExitCode == 0 {
			t.Fatalf("accepted %#v", message)
		}
	}
}
