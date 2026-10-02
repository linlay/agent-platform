package agentcreation

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func testTemplate() Template {
	return Template{
		Config: config.AgentCreationConfig{
			Types: map[string]config.AgentCreationTypeConfig{
				TypeGeneral: {DefaultGroups: []string{"office"}},
				TypeKBase:   {BaseToolsSet: true, BaseTools: []string{"file_read"}},
			},
			Groups: []config.AgentCreationGroupConfig{
				{Key: "office", Skills: []string{"online-docx"}, Connectors: []string{"builtin.httpx", "custom.desktop-lite"}},
				{Key: "data", Tools: []string{"web_fetch"}, Connectors: []string{"builtin.httpx", "builtin.dbx"}},
				{Key: "desktop", Connectors: []string{"builtin.desktop"}},
				{Key: "broken", Skills: []string{"gone"}, Tools: []string{"missing_tool"}, Connectors: []string{"nope"}},
			},
		},
		TypeTools: map[string][]string{TypeGeneral: {"bash", "file_read"}, TypeKBase: {"datetime"}},
	}
}

func testLookup() Lookup {
	exists := func(known ...string) func(string) bool {
		return func(name string) bool { return slices.Contains(known, name) }
	}
	return Lookup{
		SkillExists:     exists("online-docx"),
		ToolExists:      exists("web_fetch", "bash", "file_read"),
		ConnectorExists: exists("builtin.httpx", "builtin.dbx", "builtin.desktop", "custom.desktop-lite"),
		ConnectorConflict: func(ids []string) error {
			if slices.Contains(ids, "builtin.desktop") && slices.Contains(ids, "custom.desktop-lite") {
				return fmt.Errorf("builtin.desktop conflicts with custom.desktop-lite")
			}
			return nil
		},
	}
}

func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	var creationErr *Error
	if !errors.As(err, &creationErr) || creationErr.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func TestExpandMergesOverlappingGroupsOnce(t *testing.T) {
	got, err := testTemplate().Expand(TypeGeneral, []string{"office", "data", "office", " "}, testLookup())
	if err != nil {
		t.Fatal(err)
	}
	want := Expansion{
		Tools:      []string{"web_fetch"},
		Skills:     []string{"online-docx"},
		Connectors: []string{"builtin.httpx", "custom.desktop-lite", "builtin.dbx"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expansion = %#v, want %#v", got, want)
	}
	empty, err := testTemplate().Expand(TypeCoder, []string{}, testLookup())
	if err != nil || !reflect.DeepEqual(empty, Expansion{}) {
		t.Fatalf("deselecting every group must expand to nothing: %#v %v", empty, err)
	}
}

func TestExpandRejectsWhatCannotRun(t *testing.T) {
	template, lookup := testTemplate(), testLookup()
	_, err := template.Expand(TypeGeneral, []string{"unknown"}, lookup)
	expectCode(t, err, CodeUnknownGroup)

	_, err = template.Expand(TypeKBase, []string{"broken"}, lookup)
	expectCode(t, err, CodeGroupUnavailable)
	for _, want := range []string{"skill gone", "tool missing_tool", "connector nope"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("unavailable message must name %q: %v", want, err)
		}
	}

	_, err = template.Expand(TypeGeneral, []string{"office", "desktop"}, lookup)
	expectCode(t, err, CodeGroupConflict)

	_, err = template.Expand(TypeACP, []string{"office"}, lookup)
	expectCode(t, err, CodeGroupsUnsupported)
	if _, err := template.Expand(TypeACP, nil, lookup); err != nil {
		t.Fatalf("ACP without groups must be accepted: %v", err)
	}
	_, err = template.Expand("proxy", nil, lookup)
	expectCode(t, err, CodeGroupsUnsupported)
}

func TestBaseToolsPreferConfiguredList(t *testing.T) {
	template := testTemplate()
	if got := template.BaseTools(TypeGeneral); !reflect.DeepEqual(got, []string{"bash", "file_read"}) {
		t.Fatalf("general base tools = %v", got)
	}
	if got := template.BaseTools(TypeKBase); !reflect.DeepEqual(got, []string{"file_read"}) {
		t.Fatalf("configured base tools must replace the built-in list: %v", got)
	}
	if got := template.BaseTools(TypeCoder); len(got) != 0 {
		t.Fatalf("coder has no base tools of its own: %v", got)
	}
	if got := template.DefaultGroups(TypeGeneral); !reflect.DeepEqual(got, []string{"office"}) {
		t.Fatalf("default groups = %v", got)
	}
}

func names(definition map[string]any, section, field string) []string {
	return definitionNames(definition, section, field)
}

func TestApplyToDefinitionKeepsExplicitListsAndTypeDefaults(t *testing.T) {
	expansion := Expansion{Tools: []string{"web_fetch", "bash"}, Skills: []string{"online-docx"}, Connectors: []string{"builtin.httpx"}}
	explicit := map[string]any{
		"toolConfig":      map[string]any{"tools": []any{"regex", "bash"}},
		"connectorConfig": map[string]any{"connectors": []any{"builtin.dbx"}},
	}
	general := ApplyToDefinition(explicit, []string{"bash", "file_read"}, expansion, nil)
	if got := names(general, "toolConfig", "tools"); !reflect.DeepEqual(got, []string{"regex", "bash", "file_read", "web_fetch"}) {
		t.Fatalf("general tools = %v", got)
	}
	if got := names(general, "connectorConfig", "connectors"); !reflect.DeepEqual(got, []string{"builtin.dbx", "builtin.httpx"}) {
		t.Fatalf("connectors = %v", got)
	}
	if got := names(explicit, "toolConfig", "tools"); len(got) != 2 {
		t.Fatalf("input definition was mutated: %v", got)
	}

	coderDefaults := []string{"bash", "file_read", "file_edit"}
	// A group that adds a tool must not narrow CODER to that tool.
	coder := ApplyToDefinition(map[string]any{"mode": "CODER"}, nil, expansion, coderDefaults)
	if got := names(coder, "toolConfig", "tools"); !reflect.DeepEqual(got, []string{"bash", "file_read", "file_edit", "web_fetch"}) {
		t.Fatalf("coder tools = %v", got)
	}
	// With nothing to add, the list stays undeclared so CODER keeps following
	// its built-in default.
	untouched := ApplyToDefinition(map[string]any{"mode": "CODER"}, nil, Expansion{Skills: []string{"online-docx"}}, coderDefaults)
	if _, declared := untouched["toolConfig"]; declared {
		t.Fatalf("coder tool list must stay undeclared: %#v", untouched)
	}
	if got := names(untouched, "skillConfig", "skills"); !reflect.DeepEqual(got, []string{"online-docx"}) {
		t.Fatalf("skills = %v", got)
	}
	if bare := ApplyToDefinition(map[string]any{"mode": "GENERAL"}, nil, Expansion{}, nil); len(bare) != 1 {
		t.Fatalf("an empty expansion must not synthesize sections: %#v", bare)
	}
}

func TestLocalizedText(t *testing.T) {
	texts := map[string]string{"": "Office", "zh-cn": "文档办公", "en-us": "Office documents"}
	for locale, want := range map[string]string{"zh-CN": "文档办公", "en-US": "Office documents", "en-GB": "Office documents", "fr-FR": "Office", "": "Office"} {
		if got := LocalizedText(texts, locale); got != want {
			t.Fatalf("LocalizedText(%q) = %q, want %q", locale, got, want)
		}
	}
	if got := LocalizedText(map[string]string{"zh-cn": "文档办公"}, "en-US"); got != "文档办公" {
		t.Fatalf("fallback to any configured text failed: %q", got)
	}
}
