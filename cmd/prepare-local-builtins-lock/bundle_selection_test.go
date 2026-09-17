package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/builtins"
)

func TestExcludedGitBashNeedsNoSourceAndPreservesCanonicalLock(t *testing.T) {
	t.Setenv("BUNDLE_GIT_BASH", "false")
	root := t.TempDir()
	collection := filepath.Join(root, "collection")
	if err := os.MkdirAll(filepath.Join(collection, "ripgrep"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collection, "ripgrep/rg.exe"), []byte("rg"), 0644); err != nil {
		t.Fatal(err)
	}
	hash, err := fileSHA256(filepath.Join(collection, "ripgrep/rg.exe"))
	if err != nil {
		t.Fatal(err)
	}
	lock := builtins.Lock{SchemaVersion: 2, DefaultRoot: collection, Components: []builtins.Component{
		{Name: "rg", Version: "1.0.0", Repository: "ripgrep", Kind: "file", Required: true, Targets: map[string]builtins.Target{"windows-amd64": {Version: "1.0.0", Path: "rg.exe", Output: "rg.exe", SHA256: hash}}},
		{Name: "git-bash", Version: "v1.1.0", Repository: "git-bash", Kind: "archive-tree", Targets: map[string]builtins.Target{"windows-amd64": {Version: "v1.1.0", Path: "missing.zip", Format: "zip", SHA256: hash, Tree: &builtins.TreeLayout{Root: "runtime", Outputs: []builtins.TreeOutput{{Path: builtins.GitBashRelativeRoot, Type: "dir"}}}}}},
	}}
	input, output := filepath.Join(root, "lock.json"), filepath.Join(root, "derived.json")
	original, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, original, 0644); err != nil {
		t.Fatal(err)
	}
	if err := run(input, output, collection, []string{"windows/amd64"}); err != nil {
		t.Fatal(err)
	}
	derived, err := builtins.LoadLock(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(derived.Components) != 1 || derived.Components[0].Name != "rg" {
		t.Fatalf("unexpected derived lock: %+v", derived)
	}
	staged, err := builtins.Stage(builtins.StageOptions{RepoRoot: root, LockPath: input, BuiltinsRoot: collection, OutputDir: filepath.Join(root, "stage"), GOOS: "windows", GOARCH: "amd64", ExcludeGitBash: true})
	if err != nil {
		t.Fatal(err)
	}
	if !staged.Manifest.GitBashExcluded || len(staged.Manifest.Components) != 1 {
		t.Fatal("stage did not exclude Git Bash")
	}
	if _, err := prepareRolloutCandidate(input, collection, collection, "windows/amd64"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(input)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("canonical lock changed", err)
	}
}
