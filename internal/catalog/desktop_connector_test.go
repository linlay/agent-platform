package catalog

import (
	"agent-platform/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopMountProvidesNativeToolsWithoutBash(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), BuiltinConnectorsDir: filepath.Join(root, "builtins"), SkillsCenterDir: filepath.Join(root, "skills-center"), TeamsDir: filepath.Join(root, "teams")}}
	// No external builtin cache is required for the embedded native package.
	cfg.Paths.BuiltinConnectorsDir = ""
	release, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	for _, key := range []string{"one", "two"} {
		writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml"), "key: "+key+"\nname: Desktop\nmode: REACT\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.desktop\n")
	}
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "legacy", "agent.yml"), "key: legacy\nname: Legacy\nmode: REACT\nmodelConfig:\n  modelKey: test\ntoolConfig:\n  tools:\n    - desktop_action\n")
	r, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	one, ok := r.AgentDefinition("one")
	if !ok {
		t.Fatalf("native unavailable: %#v", r.adminAgents)
	}
	two, _ := r.AgentDefinition("two")
	if one.ConnectorMounts[0].Dir != two.ConnectorMounts[0].Dir {
		t.Fatal("duplicated native package")
	}
	if containsString(one.Tools, "bash") || !containsString(one.Tools, "file_read") || len(one.ConnectorNativeTools) != 2 || len(one.ConnectorSkills) != 2 {
		t.Fatalf("wrong native tools: %#v", one)
	}
	if _, ok := r.AgentDefinition("legacy"); ok {
		t.Fatal("legacy tool config bypassed mount")
	}
	if one.SkillInstructionsPath("desktop-cdp") != "@connectors/builtin.desktop/skills/desktop-cdp/SKILL.md" {
		t.Fatal("unstable skill path")
	}
	if _, err := os.Stat(filepath.Join(one.RuntimeDir, "connectors", "builtin.desktop", "connector.json")); !os.IsNotExist(err) {
		t.Fatal("Agent has duplicate package")
	}
}
