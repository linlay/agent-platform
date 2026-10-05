package connector

import (
	"agent-platform/internal/connectortest"
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
	if err := connectortest.WriteCLI(filepath.Join(s.BuiltinRoot, "builtin.dbx"), "dbx", "1.0.0"); err != nil {
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
		if filepath.Dir(pkgs[0].Dir) != filepath.Join(s.SharedRoot(), "builtin.dbx") || pkgs[0].PersistentRoot() != s.StateRoot {
			t.Fatal("wrong mounted metadata")
		}
		if _, err := os.Stat(filepath.Join(target, "search")); !os.IsNotExist(err) {
			t.Fatal("unmounted package copied")
		}
	}
	one, err := ReadMount(a, "builtin.dbx")
	if err != nil {
		t.Fatal(err)
	}
	two, err := ReadMount(b, "builtin.dbx")
	if err != nil {
		t.Fatal(err)
	}
	if one.Dir != two.Dir {
		t.Fatal("same version was copied per Agent")
	}
	if _, err := os.Stat(filepath.Join(a, "builtin.dbx")); !os.IsNotExist(err) {
		t.Fatal("Agent contains a full package")
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

func TestManagedLauncherUsesPersistentStateFromAgentRuntime(t *testing.T) {
	s := runtimeFixture(t)
	s.StateRoot = filepath.Join(t.TempDir(), "custom-state")
	dir := filepath.Join(s.ExternalRoot, "demo")
	putRuntimeFile(t, filepath.Join(dir, "connector.json"), `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"cli"}`)
	putRuntimeFile(t, filepath.Join(dir, "cli.json"), `{"platform":{"npmPackage":"demo"},"auth":{},"status":{},"unAuth":{}}`)
	launcher := "const fs = require('node:fs'); const path = require('node:path'); const pkgDir = path.resolve(__dirname, '..'); const manifest = {id:'demo'};\nconst state = path.join(path.dirname(pkgDir), '.state', manifest.id);\nconsole.log(state);"
	putRuntimeFile(t, filepath.Join(dir, "bin", "launcher.cjs"), launcher)
	runtimeRoot := filepath.Join(t.TempDir(), "agent", "connectors")
	if _, err := s.Materialize(runtimeRoot, []string{"demo"}); err != nil {
		t.Fatal(err)
	}
	mount, err := ReadMount(runtimeRoot, "demo")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(mount.Dir, "bin", "launcher.cjs")
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
