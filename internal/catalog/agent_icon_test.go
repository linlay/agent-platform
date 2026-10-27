package catalog

import (
	"os"
	"testing"

	"agent-platform/internal/config"
)

func TestAgentIconCreationDefaultsAreIndependent(t *testing.T) {
	for mode, name := range map[string]string{"GENERAL": "general", "CODER": "coder", "KBASE": "kbase", "TEAM": "team", "EXECUTOR": "executor", "PROXY": "proxy"} {
		t.Run(mode, func(t *testing.T) {
			input := map[string]any{"mode": mode}
			got := withAgentIconCreateDefault(input)
			if got["icon"].(map[string]any)["name"] != name {
				t.Fatalf("default icon = %#v", got["icon"])
			}
			if _, exists := input["icon"]; exists {
				t.Fatal("mutated caller definition")
			}
			explicit := withAgentIconCreateDefault(map[string]any{"mode": mode, "icon": map[string]any{"name": "general", "color": "#4370B4"}})
			icon := explicit["icon"].(map[string]any)
			if icon["name"] != "general" || icon["color"] != "#4370B4" {
				t.Fatalf("overrode explicit icon: %#v", icon)
			}
			colorOnly := withAgentIconCreateDefault(map[string]any{"mode": mode, "icon": map[string]any{"color": "#4370B4"}})["icon"].(map[string]any)
			if colorOnly["name"] != name || colorOnly["color"] != "#4370B4" {
				t.Fatalf("color-only icon: %#v", colorOnly)
			}
		})
	}
	url := "https://example.com/avatar.svg"
	if withAgentIconCreateDefault(map[string]any{"mode": "CODER", "icon": url})["icon"] != url {
		t.Fatal("external icon changed")
	}
	explicit := normalizeEditableDefinition(map[string]any{"mode": "KBASE", "icon": map[string]any{"name": "coder", "color": "#4370B4"}})
	if explicit["icon"].(map[string]any)["name"] != "coder" {
		t.Fatal("mode edit overwrote configured icon")
	}
}

func TestAgentIconCreationPersistsIndependentYAMLIdentity(t *testing.T) {
	root := t.TempDir()
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{AgentsDir: root}}}
	definition := map[string]any{"key": "independent", "name": "Independent", "mode": "CODER",
		"icon": map[string]any{"name": "proxy", "color": "#358E8A"}, "modelConfig": map[string]any{"modelKey": "test-model"}}
	created, err := registry.CreateEditableAgent("independent", definition, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(created.Source.Path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := config.LoadYAMLTreeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	icon := tree.(map[string]any)["icon"].(map[string]any)
	if icon["name"] != "proxy" || icon["color"] != "#358E8A" {
		t.Fatalf("persisted icon = %#v", icon)
	}
	definition["mode"] = "GENERAL"
	if _, err = registry.UpdateEditableAgent("independent", definition, nil, nil); err != nil {
		t.Fatal(err)
	}
	reloaded, found, err := registry.EditableAgent("independent")
	if err != nil || !found {
		t.Fatalf("reload: %v, %v", found, err)
	}
	got := reloaded.Definition["icon"].(map[string]any)
	if got["name"] != "proxy" || got["color"] != "#358E8A" {
		t.Fatalf("mode edit changed icon: %#v", got)
	}
	defaults, err := registry.CreateEditableAgent("fallback", map[string]any{"key": "fallback", "name": "Fallback", "mode": "GENERAL", "modelConfig": map[string]any{"modelKey": "test-model"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Definition["icon"].(map[string]any)["name"] != "general" {
		t.Fatalf("default not persisted: %#v", defaults.Definition)
	}
}
