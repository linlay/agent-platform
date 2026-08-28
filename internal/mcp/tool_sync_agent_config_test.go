package mcp

import (
	"path/filepath"
	"reflect"
	"testing"

	"agent-platform/internal/api"
)

func TestToolNamesForServersUsesAgentSelectedServerKeys(t *testing.T) {
	root := t.TempDir()
	writeMCPRegistryFile(t, filepath.Join(root, "alpha.yml"), "serverKey: alpha\nbaseUrl: https://alpha.example.test\n")
	writeMCPRegistryFile(t, filepath.Join(root, "beta.yml"), "serverKey: beta\nbaseUrl: https://beta.example.test\n")
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	syncer := NewToolSync(registry, nil)
	syncer.snapshots = map[string]serverToolSnapshot{
		"alpha": {toolsByName: map[string]api.ToolDetailResponse{
			"alpha_only": {Name: "alpha_only", Meta: map[string]any{"serverKey": "alpha"}},
			"shared":     {Name: "shared", Meta: map[string]any{"serverKey": "alpha"}},
		}},
		"beta": {toolsByName: map[string]api.ToolDetailResponse{
			"beta_only": {Name: "beta_only", Meta: map[string]any{"serverKey": "beta"}},
			"shared":    {Name: "shared", Meta: map[string]any{"serverKey": "beta"}},
		}},
	}
	syncer.toolsByName, syncer.aliasToCanonical = mergeSnapshots(registry.Servers(), syncer.snapshots)
	syncer.registryVersion = registry.Version()

	if got := syncer.ToolNamesForServers([]string{"ALPHA"}); !reflect.DeepEqual(got, []string{"alpha_only"}) {
		t.Fatalf("alpha tools = %#v", got)
	}
	if got := syncer.ToolNamesForServers([]string{"beta"}); !reflect.DeepEqual(got, []string{"beta_only"}) {
		t.Fatalf("beta tools = %#v", got)
	}
	if got := syncer.ToolNamesForServers([]string{"missing"}); len(got) != 0 {
		t.Fatalf("missing server tools = %#v", got)
	}
}
