package config

import (
	"path/filepath"
	"testing"
)

func TestConnectorPathsSeparateSourcesRuntimeAndState(t *testing.T) {
	root := t.TempDir()
	p := PathsConfig{AgentsDir: filepath.Join(root, "agents"), SkillsCenterDir: filepath.Join(root, "skills-center")}
	if p.EffectiveConnectorsCenterDir() != filepath.Join(root, "connectors-center") || p.EffectiveRUConnectorsDir() != filepath.Join(root, "ru-connectors") || p.EffectiveConnectorStateDir() != filepath.Join(root, "connector-state") {
		t.Fatalf("defaults: %#v", p.ConnectorSources())
	}
	if err := validateConnectorPaths(p); err != nil {
		t.Fatal(err)
	}
	p.RUConnectorsDir = filepath.Join(root, "agents", "generated")
	if err := validateConnectorPaths(p); err == nil {
		t.Fatal("generated connectors overlap Agent sources")
	}
	c := Config{Paths: PathsConfig{ConnectorsCenterDir: filepath.Join(root, "connectors-center")}}
	c.applyPathsValues(map[string]any{"connectors-dir": filepath.Join(root, "legacy-custom"), "ru-connectors-dir": filepath.Join(root, "custom-generated"), "connector-state-dir": filepath.Join(root, "custom-state")})
	if c.Paths.LegacyConnectorsDir != filepath.Join(root, "legacy-custom") || c.Paths.EffectiveRUConnectorsDir() != filepath.Join(root, "custom-generated") || c.Paths.EffectiveConnectorsCenterDir() != filepath.Join(root, "connectors-center") {
		t.Fatal("legacy path replaced the new source root")
	}
}

func TestCustomConnectorCenterRetainsDefaultLegacyRuntimeSource(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AP_RUNTIME_DIR", root)
	cfg := defaultConfig(LoadOptions{})
	cfg.applyPathsValues(map[string]any{"connectors-center-dir": filepath.Join(t.TempDir(), "external-packages")})
	if cfg.Paths.LegacyConnectorsDir != filepath.Join(root, "connectors") {
		t.Fatal("custom center changed the legacy runtime source")
	}
}
