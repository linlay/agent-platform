package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConnectorPathsSeparateSourcesRuntimeAndState(t *testing.T) {
	root := t.TempDir()
	p := PathsConfig{AgentsDir: filepath.Join(root, "agents"), SkillsCenterDir: filepath.Join(root, "skills-center")}
	if p.EffectiveConnectorsCenterDir() != filepath.Join(root, "connectors-center") || p.EffectiveStateDir() != filepath.Join(root, ".state") || p.EffectiveConnectorStateDir() != filepath.Join(root, ".state", "connectors") {
		t.Fatalf("defaults: %#v", p.ConnectorSources())
	}
	if err := validateConnectorPaths(p); err != nil {
		t.Fatal(err)
	}
	p.ConnectorsCenterDir = filepath.Join(root, "agents", "connectors-center")
	if err := validateConnectorPaths(p); err == nil {
		t.Fatal("generated connectors overlap Agent sources")
	}

}

func TestPlatformStateRootRejectsOverlapAndSymlinks(t *testing.T) {
	root := t.TempDir()
	p := PathsConfig{AgentsDir: filepath.Join(root, "agents"), ChatsDir: filepath.Join(root, "chats")}
	for _, state := range []string{root, p.AgentsDir, filepath.Join(p.ChatsDir, "private"), filepath.Join(p.EffectiveConnectorsCenterDir(), "private"), filepath.Join(p.EffectiveRUAgentsDir(), "private")} {
		p.StateDir = state
		if err := validateConnectorPaths(p); err == nil {
			t.Fatalf("state overlap accepted: %s", state)
		}
	}
	p.StateDir = filepath.Join(root, ".state")
	if err := os.Symlink(t.TempDir(), p.StateDir); err != nil {
		t.Skip(err)
	}
	if err := validateConnectorPaths(p); err == nil {
		t.Fatal("linked Platform state root accepted")
	}
}
