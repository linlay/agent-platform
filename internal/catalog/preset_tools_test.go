package catalog

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

func TestPresetToolsResolveAndExclude(t *testing.T) {
	d := AgentDefinition{Engine: AgentEngineNative, Mode: AgentModeGeneral, Tools: []string{"datetime", "bash", "sleep"}, ExcludedTools: []string{"sleep", "bash", "file_read"}}
	d.applyPresetTools([]string{"datetime", "sleep", "file_read"})
	if !reflect.DeepEqual(d.Tools, []string{"datetime"}) {
		t.Fatalf("tools=%v", d.Tools)
	}
	if len(d.DeclaredTools) != 3 || len(d.ToolBindings) != 4 {
		t.Fatalf("lost provenance: %#v", d)
	}
	// Even an excluded file_read is provided by the native Desktop connector.
	if err := resolveConnectorPackages(&d, func(string) (connector.Package, error) { return connector.Package{}, nil }); err != nil {
		t.Fatal(err)
	}
	d.addAutomaticToolBinding("file_read", "connector")
	if d.ToolBindings[2].Removable || !d.ToolBindings[2].Excluded || !d.ToolBindings[2].Active {
		t.Fatalf("binding=%#v", d.ToolBindings[2])
	}
	d.Skills = []string{"skill"}
	d.finishToolBindings()
	if !containsString(d.Tools, "bash") {
		t.Fatal("runtime dependency removed")
	}
	clone := cloneAgentDefinitionSnapshot(d)
	clone.ToolBindings[0].Source = "changed"
	clone.ExcludedTools[0] = "changed"
	if d.ToolBindings[0].Source != "preset" || d.ExcludedTools[0] != "sleep" {
		t.Fatal("snapshot alias")
	}
	for _, isolated := range []AgentDefinition{{Engine: AgentEngineACP}, {Mode: "TEAM"}} {
		isolated.applyPresetTools([]string{"datetime"})
		if len(isolated.Tools) != 0 {
			t.Fatal("injected outside ordinary native Agent")
		}
	}
}
func TestPresetToolsValidateRegistration(t *testing.T) {
	defs := []api.ToolDetailResponse{{Name: "datetime"}, {Name: "remote", Meta: map[string]any{"sourceType": "mcp"}}, {Name: "agent_delegate"}, {Name: "desktop_action"}}
	for _, name := range []string{"missing", "remote", "agent_delegate", "desktop_action"} {
		if validatePresetTools([]string{name}, defs) == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	if err := validatePresetTools([]string{"datetime"}, defs); err != nil {
		t.Fatal(err)
	}
}
func TestPresetToolsCatalogAndStructuredSave(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{PresetTools: []string{"datetime", "sleep"}, Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), SkillsCenterDir: filepath.Join(root, "skills"), ChatsDir: filepath.Join(root, "chats")}}
	r, err := NewFileRegistry(cfg, []api.ToolDetailResponse{{Name: "datetime"}, {Name: "sleep"}, {Name: "bash"}})
	if err != nil {
		t.Fatal(err)
	}
	definition := map[string]any{"key": "demo", "mode": "GENERAL", "modelConfig": map[string]any{"modelKey": "test"}, "toolConfig": map[string]any{"tools": []string{"datetime", "bash"}}}
	files, err := r.CreateEditableAgent("demo", definition, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := listStrings(mapNode(files.Definition["toolConfig"])["tools"]); !reflect.DeepEqual(got, []string{"bash"}) {
		t.Fatalf("preset persisted: %v", got)
	}
	// Simulate a source edit: existing explicit preset plus a source-only exclusion.
	raw := "key: demo\nmode: GENERAL\nmodelConfig:\n  modelKey: test\ntoolConfig:\n  tools:\n    - datetime\n    - bash\n  excludeTools:\n    - sleep\n"
	if err := os.WriteFile(files.Source.Path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	d, ok := r.AgentDefinition("demo")
	if !ok {
		t.Fatal("agent missing")
	}
	if !reflect.DeepEqual(d.Tools, []string{"datetime", "bash"}) || !d.ToolBindings[1].Excluded {
		t.Fatalf("resolved=%#v", d)
	}
	delete(definition, "toolConfig")
	saved, err := r.UpdateEditableAgent("demo", definition, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := listStrings(mapNode(saved.Definition["toolConfig"])["tools"]); !reflect.DeepEqual(got, []string{"datetime"}) {
		t.Fatalf("lost original declaration: %v", got)
	}
	if got := listStrings(mapNode(saved.Definition["toolConfig"])["excludeTools"]); !reflect.DeepEqual(got, []string{"sleep"}) {
		t.Fatalf("lost exclusion: %v", got)
	}
}
func TestPresetToolsACPRejectsExclusions(t *testing.T) {
	d := AgentDefinition{Engine: AgentEngineACP, ACPBridgeID: "bridge", ExcludedTools: []string{"sleep"}}
	if ValidateAgentCoderBackend(d) == nil {
		t.Fatal("ACP accepted exclusion")
	}
	d.ExcludedTools = nil
	if err := ValidateAgentCoderBackend(d); err != nil {
		t.Fatal(err)
	}
}

func TestPresetToolsModeDefaultsAndRuntimeDependencies(t *testing.T) {
	for _, mode := range []string{AgentModeGeneral, AgentModeCoder, AgentModeKBase} {
		d := AgentDefinition{Mode: mode, Engine: AgentEngineNative, DeclaredTools: []string{}, ExcludedTools: []string{"sleep", "bash"}}
		d.applyPresetTools([]string{"datetime", "sleep"})
		if !containsString(d.Tools, "datetime") || containsString(d.Tools, "sleep") || containsString(d.Tools, "bash") {
			t.Fatalf("mode=%s tools=%v", mode, d.Tools)
		}
		d.KBaseConfig.Enabled = true
		d.MemoryEnabled = true
		d.finishToolBindings()
		for _, name := range []string{"kbase_search", "memory_read"} {
			if !containsString(d.Tools, name) {
				t.Fatalf("lost %s in %s", name, mode)
			}
		}
	}
	for _, mode := range []string{"TEAM", "PROXY", "CHANNEL"} {
		d := AgentDefinition{Mode: mode}
		d.applyPresetTools([]string{"datetime"})
		if len(d.Tools) != 0 {
			t.Fatalf("injected into %s", mode)
		}
	}
}
