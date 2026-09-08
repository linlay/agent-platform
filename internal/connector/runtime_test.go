package connector

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runtimeFixture(t *testing.T) Sources {
	t.Helper()
	root := t.TempDir()
	s := Sources{ExternalRoot: filepath.Join(root, "connectors-center"), BuiltinRoot: filepath.Join(root, "platform", "connectors"), RuntimeRoot: filepath.Join(root, "ru-connectors"), StateRoot: filepath.Join(root, ".state", "connectors")}
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

func TestRuntimeAssemblySharesPackagesAndKeepsPersistentState(t *testing.T) {
	s := runtimeFixture(t)
	putRuntimeFile(t, filepath.Join(s.ExternalRoot, "search", "connector.json"), `{"id":"search","name":"Search","version":"1.0.0","type":"cli","auth_mode":"none"}`)
	putRuntimeFile(t, filepath.Join(s.ExternalRoot, "search", "cli.json"), `{}`)
	putRuntimeFile(t, filepath.Join(s.StateRoot, "search", "oauth.json"), "credential")
	packages, err := s.AssembleRuntime(nil)
	if err != nil || len(packages) != 2 {
		t.Fatalf("assemble: %#v %v", packages, err)
	}
	for _, pkg := range packages {
		if pkg.Dir != filepath.Join(s.RuntimeRoot, pkg.ID) || pkg.PersistentRoot() != s.StateRoot {
			t.Fatalf("wrong runtime source: %#v", pkg)
		}
	}
	skill := filepath.Join(s.RuntimeRoot, "builtin.dbx", "skills", "builtin-dbx", "SKILL.md")
	before, _ := os.Stat(skill)
	if _, err := s.AssembleRuntime(nil); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(skill)
	if !os.SameFile(before, after) {
		t.Fatal("unchanged shared skill was replaced")
	}
	if err := os.RemoveAll(s.RuntimeRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssembleRuntime(nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(s.StateRoot, "search", "oauth.json")); err != nil || string(data) != "credential" {
		t.Fatal("rebuild lost credentials")
	}
	if _, err := os.Stat(filepath.Join(s.RuntimeRoot, ".state")); !os.IsNotExist(err) {
		t.Fatal("state copied into runtime")
	}
	if err := os.RemoveAll(filepath.Join(s.ExternalRoot, "search")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssembleRuntime(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.RuntimeRoot, "search")); !os.IsNotExist(err) {
		t.Fatal("removed external package retained")
	}
	if _, err := os.Stat(skill); err != nil {
		t.Fatal("builtin removed with external package")
	}
}

func TestRuntimeAssemblyRejectsInvalidCandidateBeforePublication(t *testing.T) {
	s := runtimeFixture(t)
	if _, err := s.AssembleRuntime(nil); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(s.RuntimeRoot, "builtin.dbx", "skills", "builtin-dbx", "SKILL.md")
	before, _ := os.ReadFile(skill)
	putRuntimeFile(t, filepath.Join(s.BuiltinRoot, "builtin.dbx", "skills", "builtin-dbx", "SKILL.md"), string(before)+"\nUpdated\n")
	_, err := s.AssembleRuntime(func([]Package) error { return errors.New("MCP contract rejected") })
	if err == nil {
		t.Fatal("invalid source accepted")
	}
	if after, _ := os.ReadFile(skill); string(after) != string(before) {
		t.Fatal("validation failure changed published package")
	}
	if _, err := s.AssembleRuntime(nil); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(skill); string(after) == string(before) {
		t.Fatal("valid update was not published")
	}
}

func TestRuntimeRootsRejectOverlapAndLinkedRoots(t *testing.T) {
	s := runtimeFixture(t)
	s.RuntimeRoot = filepath.Join(s.ExternalRoot, "generated")
	if _, err := s.AssembleRuntime(nil); err == nil {
		t.Fatal("overlapping runtime accepted")
	}
	s.RuntimeRoot = filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(s.BuiltinRoot, s.RuntimeRoot); err != nil {
		t.Skip(err)
	}
	if _, err := s.AssembleRuntime(nil); err == nil {
		t.Fatal("linked runtime root accepted")
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

func TestManagedLauncherUsesPersistentStateFromSharedRuntime(t *testing.T) {
	s := runtimeFixture(t)
	s.StateRoot = filepath.Join(t.TempDir(), "custom-state")
	dir := filepath.Join(s.ExternalRoot, "demo")
	putRuntimeFile(t, filepath.Join(dir, "connector.json"), `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"cli"}`)
	putRuntimeFile(t, filepath.Join(dir, "cli.json"), `{"auth":{},"status":{},"unAuth":{}}`)
	launcher := "const fs = require('node:fs'); const path = require('node:path'); const pkgDir = path.resolve(__dirname, '..'); const manifest = {id:'demo'};\nconst state = path.join(path.dirname(pkgDir), '.state', manifest.id);\nconsole.log(state);"
	putRuntimeFile(t, filepath.Join(dir, "bin", "launcher.cjs"), launcher)
	if _, err := s.AssembleRuntime(nil); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(s.RuntimeRoot, "demo", "bin", "launcher.cjs")
	before, _ := os.Stat(target)
	if _, err := s.AssembleRuntime(nil); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(target)
	if !os.SameFile(before, after) {
		t.Fatal("unchanged managed runtime replaced")
	}
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
	if _, err := s.AssembleRuntime(nil); err != nil {
		t.Fatal(err)
	}
	pkg, err := s.LoadRuntime("builtin.dbx")
	if err != nil || pkg.Version != "1.0.0" {
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
