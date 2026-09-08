package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

func TestMountedConnectorImportsAllSkillsAndRemovesOnDetach(t *testing.T) {
	root := t.TempDir()
	agents := filepath.Join(root, "agents")
	connectorRoot := filepath.Join(root, "platform", "connectors")
	if err := connector.WriteBuiltin(filepath.Join(connectorRoot, "builtin.dbx"), "dbx", "1.0.0", "darwin"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(agents, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agents, "demo", "agent.yml")
	base := "key: demo\nname: Demo\nmode: REACT\nmodelConfig:\n  modelKey: test\n"
	if err := os.WriteFile(path, []byte(base+"connectorConfig:\n  connectors:\n    - builtin.dbx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: agents, BuiltinConnectorsDir: connectorRoot, ConnectorsCenterDir: filepath.Join(root, "connectors"), RUAgentsDir: filepath.Join(root, "ru-agents"), TeamsDir: filepath.Join(root, "teams"), SkillsCenterDir: filepath.Join(root, "skills-center")}}
	for _, name := range []string{"builtin-dbx", "builtin-httpx"} {
		legacy := filepath.Join(cfg.Paths.SkillsCenterDir, name)
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, "SKILL.md"), []byte("invalid retired skill"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	otherPath := filepath.Join(agents, "other", "agent.yml")
	writeRuntimeAssemblerFile(t, otherPath, strings.ReplaceAll(base, "key: demo", "key: other")+"connectorConfig:\n  connectors:\n    - builtin.dbx\n")
	sourceSkill := filepath.Join(connectorRoot, "builtin.dbx", "skills", "builtin-dbx")
	writeRuntimeAssemblerFile(t, filepath.Join(sourceSkill, ".config", "dbx", "default.json"), "default")
	writeRuntimeAssemblerFile(t, filepath.Join(agents, "demo", ".config", "dbx", "default.json"), "demo override")
	registry, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.Skills(""); len(got) != 0 {
		t.Fatalf("retired skills selectable: %#v", got)
	}
	def, ok := registry.AgentDefinition("demo")
	if !ok {
		t.Fatalf("agent unavailable: %#v", registry.adminAgents)
	}
	if len(def.Skills) != 0 || len(def.EffectiveSkills()) != 1 || len(def.ConnectorBinDirs) != 1 || len(def.ConnectorMounts) != 1 || def.ConnectorMounts[0].Dir != filepath.Join(def.RuntimeDir, "connectors", "builtin.dbx") {
		t.Fatalf("bad mounted definition %#v", def)
	}
	key := def.EffectiveSkills()[0]
	if key != "builtin-dbx" {
		t.Fatalf("connector skill ID must retain its original name: %q", key)
	}
	other, ok := registry.AgentDefinition("other")
	if !ok || other.ConnectorSkills[0].RuntimeDir == def.ConnectorSkills[0].RuntimeDir {
		t.Fatal("Agents unexpectedly share the same skill directory")
	}
	if _, err := os.Stat(filepath.Join(def.RuntimeDir, "skills", key)); !os.IsNotExist(err) {
		t.Fatal("connector skill copied into Agent runtime")
	}
	if def.SkillInstructionsPath(key) != "@connectors/builtin.dbx/skills/builtin-dbx/SKILL.md" {
		t.Fatal(def.SkillInstructionsPath(key))
	}
	assertRuntimeAssemblerContent(t, filepath.Join(def.RuntimeDir, ".config", "dbx", "default.json"), "demo override")
	assertRuntimeAssemblerContent(t, filepath.Join(other.RuntimeDir, ".config", "dbx", "default.json"), "default")

	if !def.IsConnectorSkill(key) {
		t.Fatal("lost connector origin")
	}
	if _, err := os.Stat(filepath.Join(def.ConnectorSkills[0].RuntimeDir, "references", "commands.md")); err != nil {
		t.Fatal(err)
	}
	assertRuntimeAssemblerContent(t, filepath.Join(cfg.Paths.SkillsCenterDir, key, "SKILL.md"), "invalid retired skill")
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	def, _ = registry.AgentDefinition("demo")
	if len(def.EffectiveSkills()) != 0 || len(def.ConnectorBinDirs) != 0 || len(def.ConnectorMounts) != 0 {
		t.Fatal("detach retained connector resources")
	}
	if _, err := os.Stat(filepath.Join(def.RuntimeDir, "skills", key)); !os.IsNotExist(err) {
		t.Fatal("detach retained imported skill")
	}
}

func TestOldMCPAgentFieldIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yml")
	if err := os.WriteFile(path, []byte("key: demo\nmodelConfig:\n  modelKey: test\ntoolConfig:\n  mcp-servers: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseAgentFileRaw(path); err == nil || !strings.Contains(err.Error(), "was removed") {
		t.Fatalf("old field accepted: %v", err)
	}
}

func TestConnectorSkillNameConflictsRejectAgent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		skills     []string
		connectors []string
		conflict   string
	}{
		{name: "configured", skills: []string{"shared"}, connectors: []string{"first"}, conflict: "skillConfig.skills"},
		{name: "configured case insensitive", skills: []string{"SHARED"}, connectors: []string{"first"}, conflict: "skillConfig.skills"},
		{name: "two connectors", connectors: []string{"first", "second"}, conflict: `connector "first"`},
		{name: "distinct", skills: []string{"ordinary"}, connectors: []string{"first"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, id := range tc.connectors {
				pkg := filepath.Join(root, "connectors", id)
				writeRuntimeAssemblerFile(t, filepath.Join(pkg, "connector.json"), `{"id":"`+id+`","name":"Fixture","version":"1.0.0","type":"cli","auth_mode":"none"}`)
				writeRuntimeAssemblerFile(t, filepath.Join(pkg, "cli.json"), `{}`)
				writeRuntimeAssemblerFile(t, filepath.Join(pkg, "skills", "shared", "SKILL.md"), "---\nname: shared\ndescription: Shared fixture\n---\n")
			}
			assembler := runtimeAgentAssembler{connectors: connector.Sources{ExternalRoot: filepath.Join(root, "connectors")}}
			def := AgentDefinition{Skills: tc.skills, Connectors: tc.connectors}
			err := assembler.resolveConnectors(&def)
			if tc.conflict != "" {
				if err == nil || !strings.Contains(err.Error(), `skill "shared"`) || !strings.Contains(err.Error(), "conflicts with "+tc.conflict) {
					t.Fatalf("expected skill name conflict with %s, got %v", tc.conflict, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(def.EffectiveSkills(), ","); got != "ordinary,shared" {
				t.Fatalf("effective skill IDs = %q", got)
			}
			definition, found, err := def.ResolveSkillDefinition("shared")
			if err != nil || !found || definition.Key != "shared" || definition.Name != "shared" {
				t.Fatalf("original skill name did not resolve: %#v %v %v", definition, found, err)
			}
		})
	}
}
