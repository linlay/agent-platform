package catalog

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
)

func TestEditableAgentSkipsDirectoriesWithoutAgentConfig(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(filepath.Join(agentsDir, "aaa-empty"), 0o755); err != nil {
		t.Fatalf("mkdir empty agent dir: %v", err)
	}
	targetDir := filepath.Join(agentsDir, "target-agent")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target agent dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "agent.yml"), []byte(
		"key: target-agent\n"+
			"name: Target Agent\n"+
			"mode: CODER\n"+
			"runtimeConfig:\n"+
			"  workspaceRoot: /tmp/workspace\n",
	), 0o644); err != nil {
		t.Fatalf("write target agent file: %v", err)
	}

	registry := &FileRegistry{
		cfg: config.Config{
			Paths: config.PathsConfig{AgentsDir: agentsDir},
		},
	}
	files, found, err := registry.EditableAgent("target-agent")
	if err != nil {
		t.Fatalf("EditableAgent returned error: %v", err)
	}
	if !found {
		t.Fatal("expected target agent to be editable")
	}
	if files.Source.Path != filepath.Join(targetDir, "agent.yml") {
		t.Fatalf("unexpected editable source path %q", files.Source.Path)
	}
}

func TestCreateEditableAgentCreatesPrivateConfigDirectory(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	registry := &FileRegistry{cfg: config.Config{Paths: config.PathsConfig{AgentsDir: agentsDir}}}

	if _, err := registry.CreateEditableAgent("reader", map[string]any{
		"key":         "reader",
		"name":        "Reader",
		"mode":        "ONESHOT",
		"modelConfig": map[string]any{"modelKey": "test-model"},
	}, nil, nil); err != nil {
		t.Fatalf("CreateEditableAgent returned error: %v", err)
	}
	configDir := filepath.Join(agentsDir, "reader", ".config")
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatalf("stat config directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("agent config path is not a directory: %s", configDir)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("agent config directory permissions = %o, want 700", info.Mode().Perm())
	}
}
