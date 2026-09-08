package connectormigrate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOfflineMigrationPreservesOtherStateNamespacesAndNPMLinks(t *testing.T) {
	t.Setenv("AP_RUNTIME_STATE_DIR", "")
	root := t.TempDir()
	old := filepath.Join(root, "connector-state", ".state", "demo")
	bin := filepath.Join(old, "npm", "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(old, "npm", "node_modules", "demo", "cli.js"), "cli", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../demo/cli.js", filepath.Join(bin, "demo")); err != nil {
		t.Skip(err)
	}
	if err := writeJSON(filepath.Join(old, "oauth.json"), "token", 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, ".state", "other", "session.json")
	if err := writeJSON(other, "other-module", 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := fingerprints(root)
	if err != nil {
		t.Fatal(err)
	}
	otherBefore, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := Run(root, false)
	if err != nil || preview.Applied {
		t.Fatalf("preview: %#v %v", preview, err)
	}
	after, err := fingerprints(root)
	if err != nil || before != after {
		t.Fatal("preview changed runtime", err)
	}
	result, err := Run(root, true)
	if err != nil || !result.Applied {
		t.Fatalf("apply: %#v %v", result, err)
	}
	link := filepath.Join(root, ".state", "connectors", "demo", "npm", "node_modules", ".bin", "demo")
	if value, err := os.Readlink(link); err != nil || value != "../demo/cli.js" {
		t.Fatal("npm link lost", err)
	}
	if _, err := os.ReadFile(link); err != nil {
		t.Fatal("npm link broken", err)
	}
	if _, err := os.Stat(filepath.Join(result.BackupDir, "connector-state", ".state", "demo", "oauth.json")); err != nil {
		t.Fatal("backup lost credentials", err)
	}
	otherAfter, err := os.Stat(other)
	if err != nil || !os.SameFile(otherBefore, otherAfter) {
		t.Fatal("other state module was replaced", err)
	}
	if result, err := Run(root, true); err != nil || result.Applied {
		t.Fatalf("repeat: %#v %v", result, err)
	}
}

func TestOfflineMigrationUsesStateEnvironment(t *testing.T) {
	root := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "custom-state")
	t.Setenv("AP_RUNTIME_STATE_DIR", stateRoot)
	legacy := filepath.Join(root, "registries", "mcp-servers", "search.yml")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("serverKey: search\nbaseUrl: https://example.test\nauthToken: private-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(stateRoot, "connectors", "demo")
	if err := writeJSON(filepath.Join(state, "npm", "node_modules", "demo", "cli.js"), "cli", 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(state, "npm", "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../demo/cli.js", filepath.Join(bin, "demo")); err != nil {
		t.Skip(err)
	}
	other := filepath.Join(stateRoot, "other", "session.json")
	if err := writeJSON(other, "other-module", 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	if preview, err := Run(root, false); err != nil || preview.Applied {
		t.Fatalf("preview: %#v %v", preview, err)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, "connectors", "search")); !os.IsNotExist(err) {
		t.Fatal("preview wrote credentials")
	}
	if result, err := Run(root, true); err != nil || !result.Applied {
		t.Fatalf("apply: %#v %v", result, err)
	}
	if _, err := os.ReadFile(filepath.Join(stateRoot, "connectors", "search", "credentials.json")); err != nil {
		t.Fatal("credentials did not use state environment", err)
	}
	if _, err := os.ReadFile(filepath.Join(bin, "demo")); err != nil {
		t.Fatal("existing npm link lost", err)
	}
	if after, err := os.Stat(other); err != nil || !os.SameFile(before, after) {
		t.Fatal("other state namespace changed", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".state")); !os.IsNotExist(err) {
		t.Fatal("default state directory was created despite env override")
	}
	if result, err := Run(root, true); err != nil || result.Applied {
		t.Fatalf("repeat: %#v %v", result, err)
	}
}
