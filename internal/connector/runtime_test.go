package connector

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runtimeFixture(t *testing.T) Sources {
	t.Helper()
	root := t.TempDir()
	s := Sources{ExternalRoot: filepath.Join(root, "connectors-center"), BuiltinRoot: filepath.Join(root, "platform", "connectors"), StateRoot: filepath.Join(root, ".state", "connectors")}
	if err := WriteBuiltin(filepath.Join(s.BuiltinRoot, "builtin.dbx"), "dbx", "1.0.0", "darwin"); err != nil {
		t.Fatal(err)
	}
	return s
}

func putRuntimeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentMaterializationCopiesOnlyMountsAndKeepsState(t *testing.T) {
	s := runtimeFixture(t)
	putRuntimeFile(t, filepath.Join(s.ExternalRoot, "search", "connector.json"), `{"id":"search","name":"Search","version":"1.0.0","type":"cli","auth_mode":"none"}`)
	putRuntimeFile(t, filepath.Join(s.ExternalRoot, "search", "cli.json"), `{}`)
	putRuntimeFile(t, filepath.Join(s.StateRoot, "search", "oauth.json"), "credential")
	a := filepath.Join(t.TempDir(), "agent-a", "connectors")
	b := filepath.Join(t.TempDir(), "agent-b", "connectors")
	for _, target := range []string{a, b} {
		pkgs, err := s.Materialize(target, []string{"builtin.dbx"})
		if err != nil || len(pkgs) != 1 {
			t.Fatalf("materialize: %#v %v", pkgs, err)
		}
		if pkgs[0].Dir != filepath.Join(target, "builtin.dbx") || pkgs[0].PersistentRoot() != s.StateRoot {
			t.Fatal("wrong mounted metadata")
		}
		if _, err := os.Stat(filepath.Join(target, "search")); !os.IsNotExist(err) {
			t.Fatal("unmounted package copied")
		}
	}
	rel := filepath.Join("builtin.dbx", "skills", "builtin-dbx", "SKILL.md")
	one, _ := os.Stat(filepath.Join(a, rel))
	two, _ := os.Stat(filepath.Join(b, rel))
	if os.SameFile(one, two) {
		t.Fatal("Agents share mutable file identity")
	}
	original, _ := os.ReadFile(filepath.Join(b, rel))
	putRuntimeFile(t, filepath.Join(a, rel), "changed")
	if data, _ := os.ReadFile(filepath.Join(b, rel)); string(data) != string(original) {
		t.Fatal("one Agent changed another")
	}
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(s.StateRoot, "search", "oauth.json")); err != nil || string(data) != "credential" {
		t.Fatal("Agent deletion lost state")
	}
}

func TestAgentCandidateRejectsOverlapLinksAndMissingMounts(t *testing.T) {
	s := runtimeFixture(t)
	for _, target := range []string{s.ExternalRoot, filepath.Join(s.StateRoot, "candidate"), filepath.Join(s.BuiltinRoot, "candidate")} {
		if _, err := s.Materialize(target, []string{"builtin.dbx"}); err == nil {
			t.Fatal("overlap accepted")
		}
	}
	target := filepath.Join(t.TempDir(), "connectors")
	if err := os.Symlink(s.BuiltinRoot, target); err == nil {
		if _, err := s.Materialize(target, []string{"builtin.dbx"}); err == nil {
			t.Fatal("linked target accepted")
		}
	}
	if _, err := s.Materialize(filepath.Join(t.TempDir(), "connectors"), []string{"missing"}); err == nil {
		t.Fatal("missing connector accepted")
	}
}

func TestLegacyLayoutMigrationMovesPackagesAndStateWithoutOverwriting(t *testing.T) {
	s := runtimeFixture(t)
	legacy := filepath.Join(filepath.Dir(s.ExternalRoot), "connectors")
	putRuntimeFile(t, filepath.Join(legacy, "demo", "connector.json"), "package")
	putRuntimeFile(t, filepath.Join(legacy, ".state", "demo", "oauth.json"), "token")
	putRuntimeFile(t, filepath.Join(legacy, ".credentials", "demo.json"), "secret")
	if err := s.MigrateLegacy(""); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateLegacy(""); err != nil {
		t.Fatal("repeat migration:", err)
	}
	for path, want := range map[string]string{filepath.Join(s.ExternalRoot, "demo", "connector.json"): "package", filepath.Join(s.StateRoot, "demo", "oauth.json"): "token", filepath.Join(s.StateRoot, "demo", "credentials.json"): "secret"} {
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Fatalf("migration lost %s: %v", path, err)
		}
	}
	putRuntimeFile(t, filepath.Join(legacy, "demo", "connector.json"), "conflicting package")
	if err := s.MigrateLegacy(""); err == nil {
		t.Fatal("collision overwritten")
	}
	if data, _ := os.ReadFile(filepath.Join(s.ExternalRoot, "demo", "connector.json")); string(data) != "package" {
		t.Fatal("original source lost")
	}
}

func TestLegacyStateCollisionIsDetectedBeforeMovingAnyFiles(t *testing.T) {
	s := runtimeFixture(t)
	legacy := filepath.Join(filepath.Dir(s.ExternalRoot), "connectors")
	putRuntimeFile(t, filepath.Join(legacy, ".state", "demo", "oauth.json"), "old")
	putRuntimeFile(t, filepath.Join(s.ExternalRoot, ".state", "demo", "oauth.json"), "new")
	if err := s.MigrateLegacy(""); err == nil {
		t.Fatal("overlapping state source overwritten")
	}
	if data, _ := os.ReadFile(filepath.Join(legacy, ".state", "demo", "oauth.json")); string(data) != "old" {
		t.Fatal("old state changed")
	}
	if data, _ := os.ReadFile(filepath.Join(s.ExternalRoot, ".state", "demo", "oauth.json")); string(data) != "new" {
		t.Fatal("new state changed")
	}
}

func TestManagedLauncherUsesPersistentStateFromAgentRuntime(t *testing.T) {
	s := runtimeFixture(t)
	s.StateRoot = filepath.Join(t.TempDir(), "custom-state")
	dir := filepath.Join(s.ExternalRoot, "demo")
	putRuntimeFile(t, filepath.Join(dir, "connector.json"), `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"cli"}`)
	putRuntimeFile(t, filepath.Join(dir, "cli.json"), `{"auth":{},"status":{},"unAuth":{}}`)
	launcher := "const fs = require('node:fs'); const path = require('node:path'); const pkgDir = path.resolve(__dirname, '..'); const manifest = {id:'demo'};\nconst state = path.join(path.dirname(pkgDir), '.state', manifest.id);\nconsole.log(state);"
	putRuntimeFile(t, filepath.Join(dir, "bin", "launcher.cjs"), launcher)
	runtimeRoot := filepath.Join(t.TempDir(), "agent", "connectors")
	if _, err := s.Materialize(runtimeRoot, []string{"demo"}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(runtimeRoot, "demo", "bin", "launcher.cjs")
	if data, _ := os.ReadFile(filepath.Join(dir, "bin", "launcher.cjs")); string(data) != launcher {
		t.Fatal("runtime adapter changed source")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	output, err := exec.Command(node, target).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != filepath.Join(s.StateRoot, "demo") {
		t.Fatalf("launcher state: %s %v", output, err)
	}
}

func TestLegacyBuiltinCopiesAreBackedUpOutsideCenter(t *testing.T) {
	s := runtimeFixture(t)
	legacy := filepath.Join(filepath.Dir(s.ExternalRoot), "connectors")
	putRuntimeFile(t, filepath.Join(legacy, "builtin.dbx", "preserved"), "legacy")
	putRuntimeFile(t, filepath.Join(s.ExternalRoot, "builtin.httpx", "preserved"), "legacy-center")
	if err := s.MigrateLegacy(""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"builtin.dbx", "builtin.httpx"} {
		if _, err := os.Lstat(filepath.Join(s.ExternalRoot, id)); !os.IsNotExist(err) {
			t.Fatal("legacy builtin retained in package center")
		}
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(s.ExternalRoot), ".connector-layout-backup-*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("missing migration backup: %v %v", backups, err)
	}
	for scope, want := range map[string]string{"connectors/builtin.dbx/preserved": "legacy", "connectors-center/builtin.httpx/preserved": "legacy-center"} {
		if data, err := os.ReadFile(filepath.Join(backups[0], scope)); err != nil || string(data) != want {
			t.Fatal("builtin backup lost", scope, err)
		}
	}
	runtimeRoot := filepath.Join(t.TempDir(), "agent", "connectors")
	packages, err := s.Materialize(runtimeRoot, []string{"builtin.dbx"})
	if err != nil || len(packages) != 1 || packages[0].Version != "1.0.0" {
		t.Fatal("builtin did not come from Platform", err)
	}
}

func TestLegacyStateMigrationPreservesInternalNPMLinks(t *testing.T) {
	s := runtimeFixture(t)
	legacy := filepath.Join(filepath.Dir(s.ExternalRoot), "connectors")
	state := filepath.Join(legacy, ".state", "demo")
	putRuntimeFile(t, filepath.Join(state, "npm", "node_modules", "demo", "cli.js"), "executable")
	bin := filepath.Join(state, "npm", "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../demo/cli.js", filepath.Join(bin, "demo")); err != nil {
		t.Skip(err)
	}
	if err := s.MigrateLegacy(""); err != nil {
		t.Fatal(err)
	}
	migrated := filepath.Join(s.StateRoot, "demo", "npm", "node_modules", ".bin", "demo")
	if link, err := os.Readlink(migrated); err != nil || link != "../demo/cli.js" {
		t.Fatalf("npm link changed: %q %v", link, err)
	}
	if data, err := os.ReadFile(migrated); err != nil || string(data) != "executable" {
		t.Fatalf("migrated npm entry is broken: %v", err)
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatal("old state retained")
	}
}

func TestLegacyStateMigrationRejectsCrossConnectorLinks(t *testing.T) {
	s := runtimeFixture(t)
	legacy := filepath.Join(filepath.Dir(s.ExternalRoot), "connectors")
	putRuntimeFile(t, filepath.Join(legacy, ".state", "other", "private"), "other-state")
	if err := os.MkdirAll(filepath.Join(legacy, ".state", "demo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../other/private", filepath.Join(legacy, ".state", "demo", "escape")); err != nil {
		t.Skip(err)
	}
	if err := s.MigrateLegacy(""); err == nil {
		t.Fatal("cross-connector state link accepted")
	}
}
