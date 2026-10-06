package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

func TestPresetConnectorMountAndSourceIsolation(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{PresetConnectors: []string{connector.WebControlConnectorID}, Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors"), SkillsCenterDir: filepath.Join(root, "skills")}}
	release, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, mode := range []string{AgentModeGeneral, AgentModeCoder, AgentModeKBase} {
		key := "agent-" + mode
		path := filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml")
		source := "# user comment\nkey: " + key + "\nname: Test\nmode: " + mode + "\nmodelConfig:\n  modelKey: test\n"
		source += "runtimeConfig:\n  workspaceRoot: " + t.TempDir() + "\n"
		writeRuntimeAssemblerFile(t, path, source)
	}
	r, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{AgentModeGeneral, AgentModeCoder, AgentModeKBase} {
		key := "agent-" + mode
		def, ok := r.AgentDefinition(key)
		if !ok || !reflect.DeepEqual(def.Connectors, cfg.PresetConnectors) || len(def.ConnectorSkills) != 1 || len(def.ConnectorMounts) != 1 || !containsString(def.Tools, "surface_list") {
			t.Fatalf("missing effective mount for %s: %+v (%v)", key, def, r.adminAgents)
		}
		if containsString(def.Tools, "catalog_manage") {
			t.Fatal("management automatically granted")
		}
		ids, err := r.ReadAgentConnectors(key)
		if err != nil || len(ids) != 0 {
			t.Fatalf("presets persisted %v %v", ids, err)
		}
		path := filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml")
		before, _ := os.ReadFile(path)
		for _, enabled := range []bool{false, true} {
			if _, err = r.PrepareAgentConnector(key, connector.WebControlConnectorID, enabled); !errors.Is(err, ErrPresetConnectorReadOnly) {
				t.Fatalf("preset toggle accepted: %v", err)
			}
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("preset toggle changed source")
		}
	}
	// Explicit duplicate stays in source while the effective package is unique.
	key := "agent-GENERAL"
	path := filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml")
	b, _ := os.ReadFile(path)
	b = append(b, []byte("connectorConfig:\n  connectors:\n    - builtin.web-control\n")...)
	_ = os.WriteFile(path, b, 0600)
	if err = r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	def, _ := r.AgentDefinition(key)
	if len(def.Connectors) != 1 || len(def.ConnectorSkills) != 1 {
		t.Fatal("duplicate mount")
	}
	for _, d := range []AgentDefinition{{Engine: AgentEngineACP, Mode: AgentModeCoder}, {Mode: "TEAM"}} {
		if ids := mergePresetConnectors(d, cfg.PresetConnectors); len(ids) != 0 {
			t.Fatalf("preset injected into %v", d)
		}
	}
	// Forms posting inherited capabilities do not create declarations.
	input := map[string]any{"key": "new", "mode": "GENERAL", "modelConfig": map[string]any{"modelKey": "test"}, "connectorConfig": map[string]any{"connectors": []string{connector.WebControlConnectorID}}}
	files, err := r.CreateEditableAgent("new", input, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids := listStrings(mapNode(files.Definition["connectorConfig"])["connectors"]); len(ids) != 0 {
		t.Fatalf("inherited declarations saved %v", ids)
	}
}

func TestPresetConnectorCannotBeDeletedWithoutAgents(t *testing.T) {
	r := &FileRegistry{cfg: config.Config{PresetConnectors: []string{"external.docs"}}}
	if _, err := r.ConnectorUsers("external.docs"); err == nil {
		t.Fatal("allowed deletion of globally configured connector")
	}
}

func TestPresetConnectorScopeDoesNotRequireValidAgent(t *testing.T) {
	for _, tree := range []map[string]any{{}, {"mode": "CODER"}, {"mode": "KBASE"}} {
		if ids := presetConnectorIDsForTree(tree, []string{"web"}); len(ids) != 1 {
			t.Fatalf("invalid Agent lost configured presets: %v", tree)
		}
	}
	for _, tree := range []map[string]any{{"engine": "acp"}, {"mode": "TEAM"}, {"engine": "invalid"}} {
		if ids := presetConnectorIDsForTree(tree, []string{"web"}); len(ids) != 0 {
			t.Fatalf("unexpected presets: %v", tree)
		}
	}
}
