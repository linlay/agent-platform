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
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: agents, BuiltinConnectorsDir: connectorRoot, ConnectorsDir: filepath.Join(root, "connectors"), RUAgentsDir: filepath.Join(root, "ru-agents"), TeamsDir: filepath.Join(root, "teams"), SkillsCenterDir: filepath.Join(root, "skills-center")}}
	for _, name := range []string{"builtin-dbx", "builtin-httpx"} {
		legacy := filepath.Join(cfg.Paths.SkillsCenterDir, name)
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, "SKILL.md"), []byte("invalid retired skill"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
	if len(def.Skills) != 0 || len(def.EffectiveSkills()) != 1 || len(def.ConnectorBinDirs) != 1 || len(def.ConnectorMounts) != 1 || def.ConnectorMounts[0].Dir != filepath.Join(connectorRoot, "builtin.dbx") {
		t.Fatalf("bad mounted definition %#v", def)
	}
	key := def.EffectiveSkills()[0]
	if !def.IsConnectorSkill(key) {
		t.Fatal("lost connector origin")
	}
	if _, err := os.Stat(filepath.Join(def.RuntimeDir, "skills", key, "references", "commands.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Paths.SkillsCenterDir, key)); !os.IsNotExist(err) {
		t.Fatal("connector skill entered skills center")
	}
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
