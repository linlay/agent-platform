package catalog

import (
	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopMountProvidesNativeToolsWithoutBash(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), BuiltinConnectorsDir: filepath.Join(root, "builtins"), SkillsCenterDir: filepath.Join(root, "skills-center")}}
	// No external builtin cache is required for the embedded native package.
	cfg.Paths.BuiltinConnectorsDir = ""
	release, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	for _, key := range []string{"one", "two"} {
		writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml"), "key: "+key+"\nname: Desktop\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.platform-control\n")
	}
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "legacy", "agent.yml"), "key: legacy\nname: Legacy\nmode: GENERAL\nmodelConfig:\n  modelKey: test\ntoolConfig:\n  tools:\n    - desktop_action\n")
	r, err := newVersionTestRegistry(t, cfg, nil)
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
	if containsString(one.Tools, "bash") || containsString(one.Tools, "file_read") || !containsString(one.Tools, "desktop_shell") || containsString(one.Tools, "surface_list") || len(one.ConnectorNativeTools) != 11 || len(one.ConnectorSkills) != 1 {
		t.Fatalf("wrong native tools: %#v", one)
	}
	if _, ok := r.AgentDefinition("legacy"); ok {
		t.Fatal("retired tools must require explicit migration")
	}
	if one.SkillInstructionsPath("platform-control") != "@connectors/builtin.platform-control/skills/platform-control/SKILL.md" {
		t.Fatal("unstable skill path")
	}
	if _, err := os.Stat(filepath.Join(one.RuntimeDir, "connectors", "builtin.platform-control", "connector.json")); !os.IsNotExist(err) {
		t.Fatal("Agent has duplicate package")
	}
}

func TestWebControlMountIsIndependentAndCombinesWithDesktop(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), SkillsCenterDir: filepath.Join(root, "skills-center")}}
	release, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	source := "key: web\nname: Web\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.web-control\n"
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "web", "agent.yml"), source)
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "both", "agent.yml"), "key: both\nname: Both\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.platform-control\n    - builtin.web-control\n")
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "retired", "agent.yml"), "key: retired\nname: Retired\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.desktop-web\n")
	r, err := newVersionTestRegistry(t, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := r.AgentDefinition("web")
	if !ok || len(def.ConnectorNativeTools) != 22 || len(def.ConnectorSkills) != 1 || containsString(def.Tools, "bash") || containsString(def.Tools, "file_read") {
		t.Fatalf("web-control mount: %+v", def)
	}
	// Page control alone grants no Desktop shell action.
	if containsString(def.Tools, "desktop_shell") || !containsString(def.Tools, "workpanel_open") || !containsString(def.Tools, "surface_open") || !containsString(def.Tools, "surface_observe") || !containsString(def.Tools, "surface_click") || !containsString(def.Tools, "awcp_invoke") {
		t.Fatalf("web-control tools: %v", def.Tools)
	}
	if def.SkillInstructionsPath("web-control") != "@connectors/builtin.web-control/skills/web-control/SKILL.md" {
		t.Fatal("wrong web-control skill path")
	}
	both, ok := r.AgentDefinition("both")
	if !ok || len(both.ConnectorNativeTools) != 33 || len(both.ConnectorSkills) != 2 || !containsString(both.Tools, "desktop_shell") || !containsString(both.Tools, "surface_list") {
		t.Fatalf("combined mount: %+v", both)
	}
	if _, err := r.PrepareAgentConnector("web", "builtin.platform-control", true); err != nil {
		t.Fatalf("Desktop must be addable next to web-control: %v", err)
	}
	// The former web variant no longer exists; its Agents need the offline migration.
	if _, ok := r.AgentDefinition("retired"); ok {
		t.Fatal("retired builtin.desktop-web mount became ready")
	}
}

func TestTaskControlMountIsIndependent(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), SkillsCenterDir: filepath.Join(root, "skills-center")}}
	release, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, key := range []string{"task", "platform"} {
		writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml"), "key: "+key+"\nname: Test\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin."+key+"-control\n")
	}
	r, err := newVersionTestRegistry(t, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	task, ok := r.AgentDefinition("task")
	if !ok {
		t.Fatal("task unavailable")
	}
	platform, _ := r.AgentDefinition("platform")
	for _, name := range []string{"chat_start", "chat_get_status", "chat_interrupt", "chat_steer", "chat_query", "chat_manage", "automation_query", "automation_manage"} {
		if !containsString(task.Tools, name) || containsString(platform.Tools, name) {
			t.Fatalf("tool ownership: %s", name)
		}
	}
	if len(task.ConnectorNativeTools) != 8 || containsString(task.Tools, "catalog_manage") || containsString(task.Tools, "desktop_shell") || containsString(task.Tools, "surface_list") {
		t.Fatalf("excess task grants: %v", task.Tools)
	}
	if task.SkillInstructionsPath("task-control") != "@connectors/builtin.task-control/skills/task-control/SKILL.md" {
		t.Fatal("missing task skill")
	}
}

func TestKanbanControlMountIsIndependent(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), SkillsCenterDir: filepath.Join(root, "skills-center")}}
	release, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, key := range []string{"kanban", "platform"} {
		writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml"), "key: "+key+"\nname: Test\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin."+key+"-control\n")
	}
	r, err := newVersionTestRegistry(t, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	task, ok := r.AgentDefinition("kanban")
	if !ok {
		t.Fatal("task unavailable")
	}
	platform, _ := r.AgentDefinition("platform")
	for _, name := range []string{"desktop_kanban"} {
		if !containsString(task.Tools, name) || containsString(platform.Tools, name) {
			t.Fatalf("tool ownership: %s", name)
		}
	}
	if len(task.ConnectorNativeTools) != 1 || containsString(task.Tools, "catalog_manage") || containsString(task.Tools, "desktop_shell") || containsString(task.Tools, "surface_list") {
		t.Fatalf("excess task grants: %v", task.Tools)
	}
	if task.SkillInstructionsPath("kanban-control") != "@connectors/builtin.kanban-control/skills/kanban-control/SKILL.md" {
		t.Fatal("missing task skill")
	}
}

func TestKBaseControlMountAndExclusion(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		ModePresets: map[string]config.AgentPresets{"kbase": {Connectors: []string{connector.KBaseControlConnectorID}}},
		Paths:       config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), SkillsCenterDir: filepath.Join(root, "skills-center")},
	}
	release, err := cfg.Paths.PrepareNativeConnectors()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	head := "\nname: Test\nmodelConfig:\n  modelKey: test\n"
	// The KBASE preset mounts the connector; GENERAL mounts explicitly and excludes one tool.
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "preset", "agent.yml"), "key: preset"+head+"mode: KBASE\nkbaseConfig:\n  libraryId: docs\nruntimeConfig:\n  workspaceRoot: "+filepath.ToSlash(workspace)+"\n")
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "partial", "agent.yml"), "key: partial"+head+"mode: GENERAL\ntoolConfig:\n  excludeTools:\n    - kbase_status\nconnectorConfig:\n  connectors:\n    - builtin.kbase-control\n")
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "plain", "agent.yml"), "key: plain"+head+"mode: GENERAL\n")
	// A standalone declaration no longer grants the tool.
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "legacy", "agent.yml"), "key: legacy"+head+"mode: GENERAL\ntoolConfig:\n  tools:\n    - kbase_search\n")
	r, err := newVersionTestRegistry(t, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	preset, ok := r.AgentDefinition("preset")
	if !ok || len(preset.ConnectorNativeTools) != 5 {
		t.Fatalf("preset mount: %v %v", ok, preset.ConnectorNativeTools)
	}
	for _, name := range []string{"kbase_list", "kbase_search", "kbase_files", "kbase_read", "kbase_status"} {
		if !containsString(preset.Tools, name) {
			t.Fatalf("preset is missing %s: %v", name, preset.Tools)
		}
	}
	if preset.SkillInstructionsPath("kbase-control") != "@connectors/builtin.kbase-control/skills/kbase-control/SKILL.md" {
		t.Fatal("missing kbase-control skill")
	}
	partial, ok := r.AgentDefinition("partial")
	if !ok || len(partial.ConnectorNativeTools) != 4 || containsString(partial.Tools, "kbase_status") || containsString(partial.ConnectorNativeTools, "kbase_status") || !containsString(partial.Tools, "kbase_search") {
		t.Fatalf("exclusion must remove the connector tool: %v %v", partial.Tools, partial.ConnectorNativeTools)
	}
	found := false
	for _, binding := range partial.EffectiveToolBindings() {
		if binding.Name == "kbase_status" {
			found = binding.Source == "connector" && binding.Excluded && !binding.Active
		}
	}
	if !found {
		t.Fatalf("excluded connector tool must stay listed: %+v", partial.EffectiveToolBindings())
	}
	if plain, _ := r.AgentDefinition("plain"); containsString(plain.Tools, "kbase_search") {
		t.Fatalf("unmounted Agent received knowledge tools: %v", plain.Tools)
	}
	if _, ok := r.AgentDefinition("legacy"); ok {
		t.Fatal("standalone kbase tool declaration must be rejected")
	}
	if err := validatePresetTools([]string{"kbase_search"}, []api.ToolDetailResponse{{Name: "kbase_search"}}); err == nil || !strings.Contains(err.Error(), "preset-connectors") {
		t.Fatalf("preset-tools must reject connector tools: %v", err)
	}
}
