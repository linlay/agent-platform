package catalog

import (
	"agent-platform/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestModeDetailAppliesToExistingAgentsWithoutPersisting(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ModeDefaultDetails: map[string]string{"coder": "full"}, Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), SkillsCenterDir: filepath.Join(root, "skills")}}
	for _, key := range []string{"inherited", "override"} {
		source := "key: " + key + "\nname: Test\nmode: CODER\nmodelConfig:\n  modelKey: test\n"
		if key == "override" {
			source += "interactionConfig:\n  defaultDetail: summary\n"
		}
		writeRuntimeAssemblerFile(t, filepath.Join(cfg.Paths.AgentsDir, key, "agent.yml"), source)
	}
	reg, err := newVersionTestRegistry(t, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, want string }{{"inherited", "full"}, {"override", "summary"}} {
		def, ok := reg.AgentDefinition(tc.key)
		if !ok || def.Interaction().DefaultDetail != tc.want {
			t.Fatalf("%s: %+v %v", tc.key, def, ok)
		}
	}
	source, err := os.ReadFile(filepath.Join(cfg.Paths.AgentsDir, "inherited", "agent.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != "key: inherited\nname: Test\nmode: CODER\nmodelConfig:\n  modelKey: test\n" {
		t.Fatalf("source mutated: %s", source)
	}
}
