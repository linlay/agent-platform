package app

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

func TestKBaseCatalogSourceExposesOnlyEnabledCapabilities(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	teamsDir := filepath.Join(root, "teams")
	skillsDir := filepath.Join(root, "skills")
	knowledgeDir := filepath.Join(root, "knowledge")
	for _, dir := range []string{agentsDir, teamsDir, skillsDir, knowledgeDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeAgent := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(agentsDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeAgent("active.yml", "key: active\nmode: REACT\nmodelConfig:\n  modelKey: mock-model\nkbaseConfig:\n  enabled: true\n  source:\n    root: "+filepath.ToSlash(knowledgeDir)+"\n")
	writeAgent("disabled.yml", "key: disabled\nmode: REACT\nmodelConfig:\n  modelKey: mock-model\nkbaseConfig:\n  enabled: false\n")

	registry, err := catalog.NewFileRegistry(config.Config{Paths: config.PathsConfig{
		AgentsDir: agentsDir, TeamsDir: teamsDir, SkillsMarketDir: skillsDir,
	}}, nil)
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	source := kbaseCatalogSource{registry: registry}

	spec, ok := source.Agent("active")
	if !ok {
		t.Fatal("enabled KBASE capability was not exposed")
	}
	if spec.Key != "active" || !spec.Config.Enabled || spec.Config.Source.Root != filepath.Clean(knowledgeDir) {
		t.Fatalf("unexpected active spec: %#v", spec)
	}
	if _, ok := source.Agent("disabled"); ok {
		t.Fatal("disabled KBASE capability was exposed")
	}
	if specs := source.Agents(); len(specs) != 1 || specs[0].Key != "active" {
		t.Fatalf("active specs = %#v", specs)
	}
}
