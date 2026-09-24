package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunConnectorSnapshotRetainsVersionAcrossRestart(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), BuiltinConnectorsDir: filepath.Join(root, "builtins"), StateDir: filepath.Join(root, ".state"), TeamsDir: filepath.Join(root, "teams"), SkillsCenterDir: filepath.Join(root, "skills")}}
	if err := connector.WriteBuiltin(filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin.desktop"), "desktop", "", "darwin"); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(cfg.Paths.AgentsDir, "demo")
	os.MkdirAll(agentDir, 0700)
	os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte("key: demo\nname: Demo\nmode: REACT\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin.desktop\n"), 0600)
	registry, err := catalog.NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	old, release, ok := registry.AcquireAgentRuntime("demo")
	defer release()
	if !ok {
		t.Fatal("missing Agent")
	}
	s := &Server{}
	s.deps.Config = cfg
	if err := s.freezeRunConnectors(preparedQuery{req: api.QueryRequest{RunID: "frozen"}, agentDef: old}); err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin.desktop", "skills", "desktop-action", "references", "version.md")
	os.WriteFile(changed, []byte("new version"), 0600)
	if err := registry.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	current, _ := registry.AgentDefinition("demo")
	if current.ConnectorMounts[0].Dir == old.ConnectorMounts[0].Dir {
		t.Fatal("new run kept old version")
	}
	fresh, err := catalog.NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := fresh.AgentDefinition("demo")
	if err := s.restoreRunConnectors("frozen", &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ConnectorMounts[0].Dir != old.ConnectorMounts[0].Dir {
		t.Fatal("restart silently switched package version")
	}
	if len(fresh.ConnectorRuntimes()) < 2 {
		t.Fatal("old MCP/runtime version absent after restart")
	}
}
