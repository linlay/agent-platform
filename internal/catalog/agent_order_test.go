package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
)

func writeOrderTestAgent(t *testing.T, agentsDir string, key string) {
	t.Helper()
	agentDir := filepath.Join(agentsDir, key)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent %s: %v", key, err)
	}
	content := strings.Join([]string{
		"key: " + key,
		"name: " + key,
	}, "\n")
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write agent %s: %v", key, err)
	}
}

func TestFileRegistryAgentsUseAgentOrderFile(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	for _, key := range []string{"alpha", "bravo", "charlie", "delta"} {
		writeOrderTestAgent(t, agentsDir, key)
	}
	registry, err := NewFileRegistry(config.Config{
		Paths: config.PathsConfig{AgentsDir: agentsDir},
	}, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	gotDefault := registry.Agents("")
	if keys := agentSummaryKeys(gotDefault); !reflect.DeepEqual(keys, []string{"alpha", "bravo", "charlie", "delta"}) {
		t.Fatalf("default ordered keys = %#v", keys)
	}

	if err := os.WriteFile(filepath.Join(agentsDir, AgentOrderFileName), []byte(`{
  "version": 1,
  "order": ["charlie", "missing", "alpha"],
  "updatedAt": 1000
}`), 0o644); err != nil {
		t.Fatalf("write order: %v", err)
	}
	gotOrdered := registry.Agents("")
	if keys := agentSummaryKeys(gotOrdered); !reflect.DeepEqual(keys, []string{"charlie", "alpha", "bravo", "delta"}) {
		t.Fatalf("file ordered keys = %#v", keys)
	}
}

func agentSummaryKeys(items []api.AgentSummary) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Key)
	}
	return keys
}
