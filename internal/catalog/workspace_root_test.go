package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHostRootWorkspacePreservesRuntimeAndPublicIdentity(t *testing.T) {
	for _, value := range []string{"@root", "/", t.TempDir(), ""} {
		t.Run(value, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "agent.yml")
			data := []byte(fmt.Sprintf("key: general\nmode: REACT\nmodelConfig:\n  modelKey: mock-model\nruntimeConfig:\n  workspaceRoot: %q\n", value))
			if err := os.WriteFile(config, data, 0600); err != nil {
				t.Fatal(err)
			}
			def, raw, err := parseAgentFileRaw(config)
			if err != nil {
				t.Fatal(err)
			}
			if got := mapNode(raw["runtimeConfig"])["workspaceRoot"]; got != value {
				t.Fatalf("raw config changed: %v", got)
			}
			if def.Workspace.HostRoot != (value == "@root") {
				t.Fatalf("lost root intent: %#v", def.Workspace)
			}
			if value == "@root" {
				want := "/"
				if runtime.GOOS == "windows" {
					cwd, err := os.Getwd()
					if err != nil {
						t.Fatal(err)
					}
					want = filepath.VolumeName(cwd) + string(os.PathSeparator)
				}
				if def.Workspace.Root != want {
					t.Fatalf("runtime root = %q, want %q", def.Workspace.Root, want)
				}
			}
			registry := &FileRegistry{agents: map[string]AgentDefinition{"general": def}}
			items := registry.Agents("nav")
			if len(items) != 1 {
				t.Fatalf("items = %#v", items)
			}
			encoded, err := json.Marshal(items[0])
			if err != nil {
				t.Fatal(err)
			}
			var public map[string]any
			if err := json.Unmarshal(encoded, &public); err != nil {
				t.Fatal(err)
			}
			_, exists := public["workspaceDir"]
			if exists != (value != "@root" && value != "") {
				t.Fatalf("public project identity: %s", encoded)
			}
			if exists && public["workspaceDir"] != def.Workspace.Root {
				t.Fatalf("project path changed: %s", encoded)
			}
		})
	}
}
