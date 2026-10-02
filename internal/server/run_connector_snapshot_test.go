package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

func TestRunConnectorSnapshotRetainsVersionAcrossRestart(t *testing.T) {
	for _, name := range []string{"desktop", "desktop-web"} {
		t.Run(name, func(t *testing.T) { testRunConnectorSnapshotRetainsVersion(t, name) })
	}
}

func testRunConnectorSnapshotRetainsVersion(t *testing.T, name string) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), ConnectorsCenterDir: filepath.Join(root, "connectors-center"), BuiltinConnectorsDir: filepath.Join(root, "builtins"), StateDir: filepath.Join(root, ".state"), TeamsDir: filepath.Join(root, "teams"), SkillsCenterDir: filepath.Join(root, "skills")}}
	if err := connector.WriteBuiltin(filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin."+name), name, ""); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(cfg.Paths.AgentsDir, "demo")
	os.MkdirAll(agentDir, 0700)
	os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte("key: demo\nname: Demo\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nconnectorConfig:\n  connectors:\n    - builtin."+name+"\n"), 0600)
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
	bindTestRuntime(s)
	if err := s.freezeRunConnectors(preparedQuery{Req: api.QueryRequest{RunID: "frozen"}, AgentDef: old}); err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(cfg.Paths.BuiltinConnectorsDir, "builtin."+name, "skills", "desktop-action", "references", "version.md")
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
	if got := runtimeNativeConnectorTools(restored); got["desktop_action"] != "builtin."+name || got["desktop_cdp"] != "builtin."+name {
		t.Fatalf("restored authorization: %v", got)
	}
	if len(fresh.ConnectorRuntimes()) < 2 {
		t.Fatal("old MCP/runtime version absent after restart")
	}
}
