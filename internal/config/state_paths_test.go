package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateDirectoryEnvironmentAndFixedRuntimeLayout(t *testing.T) {
	configRoot := t.TempDir()
	runtimeRoot := filepath.Join(configRoot, "deployment")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, value, want string }{
		{"empty", "", filepath.Join(runtimeRoot, ".state")},
		{"whitespace", "  ", filepath.Join(runtimeRoot, ".state")},
		{"relative", "custom state", filepath.Join(configRoot, "custom state")},
		{"absolute", filepath.Join(configRoot, "absolute-state"), filepath.Join(configRoot, "absolute-state")},
		{"home", "~/.agent-platform-state-test", filepath.Join(home, ".agent-platform-state-test")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withIsolatedEnv(t, map[string]string{"AP_RUNTIME_DIR": runtimeRoot, "AP_RUNTIME_STATE_DIR": tc.value, "AP_RUNTIME_AGENTS_DIR": "ignored-agent-root", "AP_RUNTIME_CONNECTORS_DIR": "ignored-connectors-root"}, func() {
				cfg, err := Load(LoadOptions{ConfigDir: configRoot})
				if err != nil {
					t.Fatal(err)
				}
				if cfg.Paths.EffectiveStateDir() != tc.want || cfg.Paths.EffectiveConnectorStateDir() != filepath.Join(tc.want, "connectors") {
					t.Fatalf("wrong state layout: %#v", cfg.Paths)
				}
				if managementState, err := ResolveStateDir(configRoot, runtimeRoot); err != nil || managementState != tc.want {
					t.Fatalf("CLI state differs from Platform: %q %v", managementState, err)
				}
				for path, child := range map[string]string{
					cfg.Paths.AgentsDir: "agents", cfg.Paths.RUAgentsDir: "ru-agents",
					cfg.Paths.TeamsDir: "teams", cfg.Paths.ToolsDir: "tools",
					cfg.Paths.OwnerDir: "owner", cfg.Paths.RootDir: "root",
					cfg.Paths.AutomationsDir: "automations", cfg.Paths.SkillsCenterDir: "skills-center",
					cfg.Paths.ConnectorsCenterDir: "connectors-center",
					cfg.Paths.LegacyConnectorsDir: "connectors", cfg.Paths.LegacyConnectorStateDir: "connector-state",
				} {
					if path != filepath.Join(runtimeRoot, child) {
						t.Fatalf("%s escaped fixed runtime layout: %s", child, path)
					}
				}
			})
		})
	}
}

func TestStateEnvironmentCannotOverlapRuntimeSources(t *testing.T) {
	root := t.TempDir()
	withIsolatedEnv(t, map[string]string{"AP_RUNTIME_DIR": root, "AP_RUNTIME_STATE_DIR": filepath.Join(root, "agents")}, func() {
		if _, err := Load(LoadOptions{ConfigDir: t.TempDir()}); err == nil {
			t.Fatal("state environment bypassed overlap validation")
		}
	})
}
