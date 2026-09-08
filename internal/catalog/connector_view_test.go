package catalog

import (
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
)

func TestPureViewMountDoesNotGrantExecution(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), ConnectorsCenterDir: filepath.Join(root, "connectors"), RUAgentsDir: filepath.Join(root, "ru-agents"), TeamsDir: filepath.Join(root, "teams"), SkillsCenterDir: filepath.Join(root, "skills")}}
	writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, "demo", "agent.yml"), "key: demo\nname: Demo\nmode: REACT\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - forms\n")
	dir := filepath.Join(cfg.Paths.ConnectorsCenterDir, "forms")
	for path, data := range map[string]string{
		"connector.json":  `{"id":"forms","name":"Forms","version":"1.0.0","type":"view","auth_mode":"none"}`,
		"view.json":       `{"views":{"card":{"renderer":"html","usage":["display"],"entry":"views/card.html"}}}`,
		"views/card.html": "<p>card</p>",
		"bin/unlisted":    "not an execution capability",
	} {
		writeRuntimeAssemblerFile(t, filepath.Join(dir, path), data)
	}
	registry, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := registry.AgentDefinition("demo")
	if !ok || len(def.ConnectorMounts) != 1 {
		t.Fatalf("view mount missing: %#v", def)
	}
	if len(def.ConnectorBinDirs) != 0 || containsString(def.Tools, "bash") {
		t.Fatalf("pure view granted execution: %#v", def)
	}
	assertRuntimeAssemblerContent(t, filepath.Join(def.RuntimeDir, "connectors", "forms", "views", "card.html"), "<p>card</p>")
}
