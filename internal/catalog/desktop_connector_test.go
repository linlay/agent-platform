package catalog

import (
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"errors"
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
	if containsString(one.Tools, "bash") || !containsString(one.Tools, "file_read") || len(one.ConnectorNativeTools) != 2 || len(one.ConnectorSkills) != 2 {
		t.Fatalf("wrong native tools: %#v", one)
	}
	legacy, ok := r.AgentDefinition("legacy")
	if !ok {
		t.Fatal("tool declaration must not require a named connector")
	}
	if len(legacy.ConnectorMounts) != 0 || len(legacy.ConnectorNativeTools) != 0 {
		t.Fatal("tool declaration must not synthesize connector execution grants")
	}
	if one.SkillInstructionsPath("desktop-cdp") != "@connectors/builtin.desktop/skills/desktop-cdp/SKILL.md" {
		t.Fatal("unstable skill path")
	}
	if _, err := os.Stat(filepath.Join(one.RuntimeDir, "connectors", "builtin.desktop", "connector.json")); !os.IsNotExist(err) {
		t.Fatal("Agent has duplicate package")
	}
}

func TestDesktopWebMountAndVariantConflict(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), SkillsCenterDir: filepath.Join(root, "skills-center"), TeamsDir: filepath.Join(root, "teams")}}
	release, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	source := "key: web\nname: Web\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.desktop-web\n"
	path := filepath.Join(cfg.Paths.AgentsDir, "web", "agent.yml")
	writeRuntimeAssemblerFile(t, path, source)
	r, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := r.AgentDefinition("web")
	if !ok || len(def.ConnectorNativeTools) != 2 || len(def.ConnectorSkills) != 2 || containsString(def.Tools, "bash") || !containsString(def.Tools, "file_read") {
		t.Fatalf("web mount: %+v", def)
	}
	if def.SkillInstructionsPath("desktop-action") != "@connectors/builtin.desktop-web/skills/desktop-action/SKILL.md" {
		t.Fatal("wrong web skill path")
	}
	if _, err := r.PrepareAgentConnector("web", "builtin.desktop", true); !errors.Is(err, connector.ErrSelectionConflict) {
		t.Fatalf("mutation conflict: %v", err)
	}
	current, err := r.ReadEditableAgentSource("web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.WriteEditableAgentSource("web", source+"    - builtin.desktop\n", current.SHA256); !errors.Is(err, connector.ErrSelectionConflict) {
		t.Fatalf("source conflict: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != source {
		t.Fatalf("rejected mutation changed source: %v", err)
	}
	// Generic validation reads declarations before importing potentially colliding skills.
	conflicting := AgentDefinition{Connectors: []string{"builtin.desktop-web", "builtin.desktop"}}
	if err := resolveConnectorPackages(&conflicting, cfg.Paths.ConnectorSources().Load); !errors.Is(err, connector.ErrSelectionConflict) {
		t.Fatalf("runtime conflict: %v", err)
	}
}

func TestDesktopSelectionCanRepairConflictingSource(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), SkillsCenterDir: filepath.Join(root, "skills"), TeamsDir: filepath.Join(root, "teams")}}
	release, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	source := "key: demo\nname: Demo\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.desktop\n    - builtin.desktop-web\n"
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "demo", "agent.yml"), source)
	r, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.AgentDefinition("demo"); ok {
		t.Fatal("conflicting Agent became ready")
	}
	ids, err := r.ReadAgentConnectors("demo")
	if err != nil || len(ids) != 2 {
		t.Fatalf("cannot display invalid selection: %v %v", ids, err)
	}
	candidate, err := r.PrepareAgentConnector("demo", "builtin.desktop", false)
	if err != nil || len(candidate.ConnectorIDs) != 1 || candidate.ConnectorIDs[0] != "builtin.desktop-web" {
		t.Fatalf("cannot repair: %+v %v", candidate, err)
	}
}
