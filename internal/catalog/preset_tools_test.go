package catalog

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/kbase"
)

func TestPresetToolsResolveAndExclude(t *testing.T) {
	d := AgentDefinition{Engine: AgentEngineNative, Mode: AgentModeGeneral, Tools: []string{"datetime", "bash", "wait"}, ExcludedTools: []string{"wait", "bash", "file_read"}}
	d.applyPresetTools([]string{"datetime", "wait", "file_read"})
	if !reflect.DeepEqual(d.Tools, []string{"datetime"}) {
		t.Fatalf("tools=%v", d.Tools)
	}
	if len(d.DeclaredTools) != 3 || len(d.ToolBindings) != 4 {
		t.Fatalf("lost provenance: %#v", d)
	}
	clone := cloneAgentDefinitionSnapshot(d)
	clone.ToolBindings[0].Source = "changed"
	clone.ExcludedTools[0] = "changed"
	if d.ToolBindings[0].Source != "preset" || d.ExcludedTools[0] != "wait" {
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
	defs := []api.ToolDetailResponse{{Name: "datetime"}, {Name: "remote", Meta: map[string]any{"sourceType": "mcp"}}, {Name: "agent_delegate"}, {Name: "desktop_shell"}}
	for _, name := range []string{"missing", "remote", "agent_delegate", "desktop_shell"} {
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
	cfg := config.Config{PresetTools: []string{"datetime", "wait"}, Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), SkillsCenterDir: filepath.Join(root, "skills"), ChatsDir: filepath.Join(root, "chats")}}
	r, err := NewFileRegistry(cfg, []api.ToolDetailResponse{{Name: "datetime"}, {Name: "wait"}, {Name: "bash"}})
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
	raw := "key: demo\nmode: GENERAL\nmodelConfig:\n  modelKey: test\ntoolConfig:\n  tools:\n    - datetime\n    - bash\n  excludeTools:\n    - wait\n"
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
	if got := listStrings(mapNode(saved.Definition["toolConfig"])["excludeTools"]); !reflect.DeepEqual(got, []string{"wait"}) {
		t.Fatalf("lost exclusion: %v", got)
	}
}
func TestPresetToolsACPRejectsExclusions(t *testing.T) {
	d := AgentDefinition{Engine: AgentEngineACP, ACPBridgeID: "bridge", ExcludedTools: []string{"wait"}}
	if ValidateAgentCoderBackend(d) == nil {
		t.Fatal("ACP accepted exclusion")
	}
	d.ExcludedTools = nil
	if err := ValidateAgentCoderBackend(d); err != nil {
		t.Fatal(err)
	}
}

func TestPresetToolsDoNotDeriveToolsFromCapabilities(t *testing.T) {
	for _, mode := range []string{AgentModeGeneral, AgentModeCoder, AgentModeKBase} {
		d := AgentDefinition{Mode: mode, Engine: AgentEngineNative, DeclaredTools: []string{}, ExcludedTools: []string{"wait", "bash"}, Skills: []string{"skill"}, MemoryEnabled: true, KBaseConfig: kbase.Config{Enabled: true}, Runtime: map[string]any{"env": map[string]string{"LANG": "en_US"}}}
		d.applyPresetTools([]string{"datetime", "wait"})
		if !containsString(d.Tools, "datetime") || containsString(d.Tools, "wait") || containsString(d.Tools, "bash") {
			t.Fatalf("mode=%s tools=%v", mode, d.Tools)
		}
		for _, name := range []string{"kbase_search", "memory_read"} {
			if containsString(d.Tools, name) {
				t.Fatalf("implicit %s in %s", name, mode)
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

func TestCoderRegexPresetControlsEffectiveToolsAndExclusion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		preset   bool
		excluded bool
		want     bool
	}{
		{name: "coder configured", mode: AgentModeCoder, preset: true, want: true},
		{name: "coder preset removed", mode: AgentModeCoder},
		{name: "coder agent exclusion", mode: AgentModeCoder, preset: true, excluded: true},
		{name: "general unaffected", mode: AgentModeGeneral, preset: true},
		{name: "kbase unaffected", mode: AgentModeKBase, preset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{PresetTools: []string{"file_read", "ask_user_question"}, ModePresets: map[string]config.AgentPresets{}}
			if tc.preset {
				cfg.ModePresets["coder"] = config.AgentPresets{Tools: []string{"regex"}}
			}
			def := AgentDefinition{Mode: tc.mode, Engine: AgentEngineNative}
			if tc.excluded {
				def.ExcludedTools = []string{"regex"}
			}
			def.applyPresetTools(cfg.PresetsForMode(tc.mode).Tools)
			if containsString(def.Tools, "regex") != tc.want {
				t.Fatalf("effective tools=%v want regex=%v", def.Tools, tc.want)
			}
		})
	}
}

func TestRunEnvPresetRespectsExclusions(t *testing.T) {
	for _, mode := range []string{AgentModeGeneral, AgentModeCoder, AgentModeKBase} {
		for _, presets := range [][]string{nil, {"run_env"}} {
			for _, excluded := range []bool{false, true} {
				d := AgentDefinition{Mode: mode, Engine: AgentEngineNative}
				if excluded {
					d.ExcludedTools = []string{"run_env"}
				}
				d.applyPresetTools(presets)
				if containsString(d.Tools, "run_env") != (len(presets) > 0 && !excluded) || containsString(d.DeclaredTools, "run_env") {
					t.Fatalf("%s excluded=%v tools=%v declared=%v", mode, excluded, d.Tools, d.DeclaredTools)
				}
				count := 0
				for _, binding := range d.EffectiveToolBindings() {
					if binding.Name == "run_env" {
						count++
						if binding.Active == excluded || binding.Excluded != excluded {
							t.Fatalf("binding %#v", binding)
						}
					}
				}
				if count != len(presets) {
					t.Fatalf("bindings %v", d.ToolBindings)
				}
			}
		}
	}
	for _, d := range []AgentDefinition{{Mode: AgentModeCoder, Engine: AgentEngineACP}, {Mode: "TEAM"}, {Mode: "CHANNEL"}, {Mode: "PROXY"}} {
		d.applyPresetTools(nil)
		if containsString(d.Tools, "run_env") {
			t.Fatalf("non-native mount %#v", d)
		}
	}
	definition := map[string]any{"mode": "GENERAL", "toolConfig": map[string]any{"tools": []string{"run_env", "bash"}}}
	stripPresetToolDeclarations(definition, nil, nil)
	if got := listStrings(mapNode(definition["toolConfig"])["tools"]); !reflect.DeepEqual(got, []string{"run_env", "bash"}) {
		t.Fatalf("explicit tool lost %v", got)
	}
}

func TestModePresetsAssemblyAndSourceEditing(t *testing.T) {
	cfg := config.Config{PresetTools: []string{"datetime"}, ModePresets: map[string]config.AgentPresets{
		"coder": {Tools: []string{"datetime", "file_read"}},
	}}
	for _, mode := range []string{AgentModeGeneral, AgentModeCoder, AgentModeKBase} {
		def := AgentDefinition{Mode: mode, Engine: AgentEngineNative, Tools: []string{"wait"}}
		def.applyPresetTools(cfg.PresetsForMode(mode).Tools)
		if !containsString(def.Tools, "datetime") || !containsString(def.Tools, "wait") {
			t.Fatal(def.Tools)
		}
		if containsString(def.Tools, "file_read") != (mode == AgentModeCoder) {
			t.Fatalf("mode isolation: %s %v", mode, def.Tools)
		}
	}
	definition := map[string]any{"mode": "CODER", "toolConfig": map[string]any{"tools": []any{"datetime", "file_read", "wait"}}}
	stripPresetToolDeclarations(definition, cfg.PresetsForMode("CODER").Tools, nil)
	if got := listStrings(mapNode(definition["toolConfig"])["tools"]); len(got) != 1 || got[0] != "wait" {
		t.Fatal(got)
	}
	r := &FileRegistry{cfg: config.Config{ModePresets: map[string]config.AgentPresets{"coder": {Connectors: []string{"builtin.httpx"}}}}}
	if _, err := r.ConnectorUsers("builtin.httpx"); err == nil {
		t.Fatal("mode preset connector deletion was allowed")
	}
}
