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
		writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml"), "key: "+key+"\nname: Desktop\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.desktop\n")
	}
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "legacy", "agent.yml"), "key: legacy\nname: Legacy\nmode: GENERAL\nmodelConfig:\n  modelKey: test\ntoolConfig:\n  tools:\n    - desktop_action\n")
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
	if containsString(one.Tools, "bash") || !containsString(one.Tools, "file_read") || !containsString(one.Tools, "desktop_action") || containsString(one.Tools, "surface_list") || len(one.ConnectorNativeTools) != 1 || len(one.ConnectorSkills) != 1 {
		t.Fatalf("wrong native tools: %#v", one)
	}
	legacy, ok := r.AgentDefinition("legacy")
	if !ok {
		t.Fatal("tool declaration must not require a named connector")
	}
	if len(legacy.ConnectorMounts) != 0 || len(legacy.ConnectorNativeTools) != 0 {
		t.Fatal("tool declaration must not synthesize connector execution grants")
	}
	if one.SkillInstructionsPath("desktop-action") != "@connectors/builtin.desktop/skills/desktop-action/SKILL.md" {
		t.Fatal("unstable skill path")
	}
	if _, err := os.Stat(filepath.Join(one.RuntimeDir, "connectors", "builtin.desktop", "connector.json")); !os.IsNotExist(err) {
		t.Fatal("Agent has duplicate package")
	}
}

func TestWebControlMountIsIndependentAndCombinesWithDesktop(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), SkillsCenterDir: filepath.Join(root, "skills-center"), TeamsDir: filepath.Join(root, "teams")}}
	release, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	source := "key: web\nname: Web\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.web-control\n"
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "web", "agent.yml"), source)
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "both", "agent.yml"), "key: both\nname: Both\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.desktop\n    - builtin.web-control\n")
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "retired", "agent.yml"), "key: retired\nname: Retired\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.desktop-web\n")
	r, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := r.AgentDefinition("web")
	if !ok || len(def.ConnectorNativeTools) != 15 || len(def.ConnectorSkills) != 1 || containsString(def.Tools, "bash") || !containsString(def.Tools, "file_read") {
		t.Fatalf("web-control mount: %+v", def)
	}
	// Page control alone grants no Desktop shell action.
	if containsString(def.Tools, "desktop_action") || !containsString(def.Tools, "workpanel_open") || !containsString(def.Tools, "surface_cdp") || !containsString(def.Tools, "awcp_invoke") {
		t.Fatalf("web-control tools: %v", def.Tools)
	}
	if def.SkillInstructionsPath("web-control") != "@connectors/builtin.web-control/skills/web-control/SKILL.md" {
		t.Fatal("wrong web-control skill path")
	}
	both, ok := r.AgentDefinition("both")
	if !ok || len(both.ConnectorNativeTools) != 16 || len(both.ConnectorSkills) != 2 || !containsString(both.Tools, "desktop_action") || !containsString(both.Tools, "surface_list") {
		t.Fatalf("combined mount: %+v", both)
	}
	if _, err := r.PrepareAgentConnector("web", "builtin.desktop", true); err != nil {
		t.Fatalf("Desktop must be addable next to web-control: %v", err)
	}
	// The former web variant no longer exists; its Agents need the offline migration.
	if _, ok := r.AgentDefinition("retired"); ok {
		t.Fatal("retired builtin.desktop-web mount became ready")
	}
}
