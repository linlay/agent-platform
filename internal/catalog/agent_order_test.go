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
	writeOrderTestAgentMode(t, agentsDir, key, "")
}

func writeOrderTestAgentMode(t *testing.T, agentsDir string, key string, mode string) {
	t.Helper()
	agentDir := filepath.Join(agentsDir, key)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent %s: %v", key, err)
	}
	lines := []string{
		"key: " + key,
		"name: " + key,
	}
	if mode != "" {
		lines = append(lines, "mode: "+mode)
	}
	content := strings.Join(lines, "\n")
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write agent %s: %v", key, err)
	}
}

func TestFileRegistryAgentsUseAgentOrderFile(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")

	// Create mix of CODER and REACT agents.
	writeOrderTestAgentMode(t, agentsDir, "bravo", "REACT")
	writeOrderTestAgentMode(t, agentsDir, "charlie", "REACT")
	writeOrderTestAgentMode(t, agentsDir, "coder-alpha", "CODER")
	writeOrderTestAgentMode(t, agentsDir, "coder-bravo", "CODER")

	registry, err := NewFileRegistry(config.Config{
		Paths: config.PathsConfig{AgentsDir: agentsDir},
	}, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	// Without agent-order.json, CODER agents should sort first alphabetically,
	// then REACT agents alphabetically.
	gotDefault := registry.Agents("")
	expectedDefault := []string{"coder-alpha", "coder-bravo", "bravo", "charlie"}
	if keys := agentSummaryKeys(gotDefault); !reflect.DeepEqual(keys, expectedDefault) {
		t.Fatalf("default ordered keys = %#v, want %#v", keys, expectedDefault)
	}

	// DefaultAgentKey should return the first CODER agent alphabetically.
	defaultKey := registry.DefaultAgentKey()
	if defaultKey != "coder-alpha" {
		t.Fatalf("DefaultAgentKey = %q, want %q", defaultKey, "coder-alpha")
	}

	// Write an agent-order.json — explicit order should be respected.
	if err := os.WriteFile(filepath.Join(agentsDir, AgentOrderFileName), []byte(`{
  "version": 1,
  "order": ["charlie", "missing", "coder-alpha"],
  "updatedAt": 1000
}`), 0o644); err != nil {
		t.Fatalf("write order: %v", err)
	}
	// Reload to pick up the order file.
	if err := registry.Reload(nil, ""); err != nil {
		t.Fatalf("reload: %v", err)
	}
	gotOrdered := registry.Agents("")
	expectedOrdered := []string{"charlie", "coder-alpha", "bravo", "coder-bravo"}
	if keys := agentSummaryKeys(gotOrdered); !reflect.DeepEqual(keys, expectedOrdered) {
		t.Fatalf("file ordered keys = %#v, want %#v", keys, expectedOrdered)
	}
}

func TestFileRegistryAgentsEmptyOrderFileUsesDefault(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")

	writeOrderTestAgentMode(t, agentsDir, "react-one", "REACT")
	writeOrderTestAgentMode(t, agentsDir, "coder-one", "CODER")

	// Write an agent-order.json with an empty order array.
	if err := os.WriteFile(filepath.Join(agentsDir, AgentOrderFileName), []byte(`{
  "version": 1,
  "order": [],
  "updatedAt": 1000
}`), 0o644); err != nil {
		t.Fatalf("write order: %v", err)
	}

	registry, err := NewFileRegistry(config.Config{
		Paths: config.PathsConfig{AgentsDir: agentsDir},
	}, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	// Empty order file should behave like default: CODER first.
	gotDefault := registry.Agents("")
	expectedDefault := []string{"coder-one", "react-one"}
	if keys := agentSummaryKeys(gotDefault); !reflect.DeepEqual(keys, expectedDefault) {
		t.Fatalf("empty order keys = %#v, want %#v", keys, expectedDefault)
	}
}

func TestFileRegistryAgentsAllReactDefaultAlphabetical(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")

	// All REACT agents (no CODER).
	for _, key := range []string{"alpha", "bravo", "charlie", "delta"} {
		writeOrderTestAgent(t, agentsDir, key)
	}
	registry, err := NewFileRegistry(config.Config{
		Paths: config.PathsConfig{AgentsDir: agentsDir},
	}, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	// Without any CODER agents, default is pure alphabetical.
	gotDefault := registry.Agents("")
	if keys := agentSummaryKeys(gotDefault); !reflect.DeepEqual(keys, []string{"alpha", "bravo", "charlie", "delta"}) {
		t.Fatalf("default ordered keys = %#v", keys)
	}

	// DefaultAgentKey should return first alphabetical.
	defaultKey := registry.DefaultAgentKey()
	if defaultKey != "alpha" {
		t.Fatalf("DefaultAgentKey = %q, want %q", defaultKey, "alpha")
	}
}

func TestDefaultAgentKeyPrefersCoder(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")

	// Only REACT agents.
	writeOrderTestAgentMode(t, agentsDir, "alpha", "REACT")
	writeOrderTestAgentMode(t, agentsDir, "bravo", "REACT")

	registry, err := NewFileRegistry(config.Config{
		Paths: config.PathsConfig{AgentsDir: agentsDir},
	}, nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	// Only REACT: alphabetical first.
	if key := registry.DefaultAgentKey(); key != "alpha" {
		t.Fatalf("want alpha, got %s", key)
	}
}

func agentSummaryKeys(items []api.AgentSummary) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.Key)
	}
	return keys
}