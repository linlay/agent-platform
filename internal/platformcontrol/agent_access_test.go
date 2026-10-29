package platformcontrol

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/config"
)

func TestCatalogAgentAccessChangesAlwaysRequireHuman(t *testing.T) {
	const base = "key: worker\nname: Worker\nmodelConfig:\n  modelKey: test\n"
	for _, level := range []string{"default", "auto_approve", "full_access"} {
		for _, tc := range []struct {
			name, before, after string
			manual              bool
		}{
			{"create with default", "", base + "accessConfig:\n  default: full_access\n", true},
			{"create empty block", "", base + "accessConfig: {}\n", true},
			{"add default", base, base + "accessConfig:\n  default: default\n", true},
			{"remove block", base + "accessConfig:\n  default: full_access\n", base, true},
			{"lower default", base + "accessConfig:\n  default: full_access\n", base + "accessConfig:\n  default: default\n", true},
			{"unchanged", base + "accessConfig: {default: full_access}\n", base + "accessConfig:\n  default: full_access\n", false},
			{"ordinary apply", "", base, false},
		} {
			t.Run(level+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				cfg := config.Config{Paths: config.PathsConfig{AgentsDir: root}}
				if tc.before != "" {
					if err := os.MkdirAll(filepath.Join(root, "worker"), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, "worker", "agent.yml"), []byte(tc.before), 0600); err != nil {
						t.Fatal(err)
					}
				}
				source := &adminsource.ControlService{Config: cfg, Mutations: adminsource.NewService()}
				baseline, err := source.Read(adminsource.ControlTarget{ResourceType: "agent", ResourceKey: "worker"})
				if tc.before != "" && err != nil {
					t.Fatal(err)
				}
				h := NewToolHandler(cfg, nil, nil).ConfigureControl(source, nil, nil)
				exec := controlExecution()
				exec.AccessLevel = level
				exec.RunControl.UpdateAccessLevel(level)
				args := map[string]any{"action": "apply", "args": map[string]any{"resourceType": "agent", "resourceKey": "worker", "content": tc.after, "baseRevision": baseline.BaseRevision}}
				approval, err := h.PrepareToolApproval(context.Background(), "catalog_manage", args, exec)
				if err != nil || approval == nil {
					t.Fatalf("approval=%+v err=%v", approval, err)
				}
				if approval.AllowAutoApprove == tc.manual {
					t.Fatalf("manual=%v auto=%v", tc.manual, approval.AllowAutoApprove)
				}
			})
		}
	}
}
