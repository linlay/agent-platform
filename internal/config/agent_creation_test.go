package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestShippedAgentCreationExampleParses(t *testing.T) {
	values, err := loadYAMLMap(filepath.Join("..", "..", "configs", "agent-creation.example.yml"))
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	cfg, err := parseAgentCreationConfig(values)
	if err != nil {
		t.Fatalf("example must parse: %v", err)
	}
	keys := make([]string, 0, len(cfg.Groups))
	for _, group := range cfg.Groups {
		keys = append(keys, group.Key)
		if group.Name["zh-cn"] == "" || group.Name["en-us"] == "" {
			t.Fatalf("group %s needs both locales: %#v", group.Key, group.Name)
		}
	}
	if want := []string{"office", "web-data", "app-skill-building", "automation"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("group order = %v, want %v", keys, want)
	}
	if got := cfg.Types["general"].DefaultGroups; !reflect.DeepEqual(got, []string{"office", "web-data"}) {
		t.Fatalf("general default groups = %v", got)
	}
	if cfg.Types["general"].BaseToolsSet {
		t.Fatalf("the example leaves base-tools unset so the built-in list applies")
	}
	// Only app building opts into Desktop management, required for WebApp packaging.
	for _, group := range cfg.Groups {
		if group.Key == "app-skill-building" && !reflect.DeepEqual(group.Connectors, []string{"builtin.web-control"}) {
			t.Fatalf("app building requires webpage control and Desktop packaging: %v", group.Connectors)
		}
		for _, id := range group.Connectors {
			if id == "builtin.platform-control" && group.Key != "app-skill-building" {
				t.Fatalf("group %s must not mount builtin.platform-control", group.Key)
			}
		}
	}
}

func TestParseAgentCreationConfig(t *testing.T) {
	cfg, err := parseAgentCreationConfig(map[string]any{
		"types": map[string]any{
			"General": map[string]any{"base-tools": []any{}, "default-groups": []any{"a"}},
		},
		"groups": []any{
			map[string]any{"key": "a", "name": "Plain", "tools": []any{"bash", "bash", " regex "}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	general := cfg.Types["general"]
	if !general.BaseToolsSet || len(general.BaseTools) != 0 {
		t.Fatalf("an explicit empty base-tools list must be kept distinct from an absent one: %#v", general)
	}
	if cfg.Groups[0].Name[""] != "Plain" || !reflect.DeepEqual(cfg.Groups[0].Tools, []string{"bash", "regex"}) {
		t.Fatalf("unexpected group: %#v", cfg.Groups[0])
	}

	for name, tc := range map[string]struct {
		values map[string]any
		want   string
	}{
		"bad key":        {map[string]any{"groups": []any{map[string]any{"key": "Bad Key"}}}, "lowercase letters"},
		"duplicate key":  {map[string]any{"groups": []any{map[string]any{"key": "a"}, map[string]any{"key": "a"}}}, "duplicated"},
		"unknown type":   {map[string]any{"types": map[string]any{"acp": map[string]any{}}}, "not supported"},
		"unknown group":  {map[string]any{"types": map[string]any{"coder": map[string]any{"default-groups": []any{"ghost"}}}}, "unknown group"},
		"members scalar": {map[string]any{"groups": []any{map[string]any{"key": "a", "skills": "online-docx"}}}, "must be a list"},
		"name number":    {map[string]any{"groups": []any{map[string]any{"key": "a", "name": 3}}}, "locale object"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAgentCreationConfig(tc.values); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
