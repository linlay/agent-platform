package catalog

import (
	"agent-platform/internal/config"
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLegacyContextAgentsIgnoredWithOneWarning(t *testing.T) {
	for _, legacy := range []string{
		"  tags:\n    - system\n    - agents\n    - session\n    - agents\n",
		"  agents: [missing]\n",
		"  tags: agents\n",
		"  agents: false\n",
		"  agents: {invalid: type}\n",
		"  agents: null\n",
		"  agents: 42\n",
		"  tags:\n    - agents\n  agents: '*'\n",
	} {
		t.Run(legacy, func(t *testing.T) {
			root := t.TempDir()
			cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents")}}
			path := filepath.Join(cfg.Paths.AgentsDir, "parent", "agent.yml")
			writeRuntimeAssemblerFile(t, path, "key: parent\nmode: GENERAL\nmodelConfig: {modelKey: test}\ncontextConfig:\n"+legacy)
			r, err := newVersionTestRegistry(t, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if i > 0 {
					if err := r.Reload(context.Background(), "agents"); err != nil {
						t.Fatal(err)
					}
				}
				def, ok := r.AgentDefinition("parent")
				if !ok {
					t.Fatal("legacy fields invalidated agent")
				}
				for _, tag := range def.ContextTags {
					if tag == "agents" {
						t.Fatal("legacy tag retained")
					}
				}
				item, ok := r.AdminAgent("parent")
				if !ok || item.Status != AdminAgentStatusReady || len(item.Diagnostics) != 1 || item.Diagnostics[0].Severity != "warning" || item.Diagnostics[0].Code != "context_agents_ignored" {
					t.Fatalf("admin=%#v", item)
				}
				list := r.AdminAgents()
				if len(list) != 1 || !reflect.DeepEqual(list[0].Diagnostics, item.Diagnostics) {
					t.Fatalf("list=%#v", list)
				}
			}
			writeRuntimeAssemblerFile(t, path, "key: parent\nmode: GENERAL\nmodelConfig: {modelKey: test}\ncontextConfig: {tags: [system, session]}\n")
			if err := r.Reload(context.Background(), "agents"); err != nil {
				t.Fatal(err)
			}
			item, _ := r.AdminAgent("parent")
			if len(item.Diagnostics) != 0 {
				t.Fatalf("stale warnings: %#v", item.Diagnostics)
			}
		})
	}
}
