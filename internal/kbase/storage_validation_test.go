package kbase

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageValidatorRejectsSymlinkedCanonicalOwnerConflict(t *testing.T) {
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	target := filepath.Join(root, "shared-storage")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"alpha", "beta"} {
		if err := os.Symlink(target, filepath.Join(runtimeDir, key)); err != nil {
			t.Skipf("symlink is unavailable: %v", err)
		}
	}
	source := stubAgentSource{agents: map[string]AgentSpec{
		"alpha": testKBaseAgent("alpha", filepath.Join(root, "source-alpha"), "runtime"),
		"beta":  testKBaseAgent("beta", filepath.Join(root, "source-beta"), "runtime"),
	}}
	manager := NewManager(ManagerOptions{RuntimeDir: runtimeDir}, source, nil)
	err := manager.ValidateConfiguration()
	if err == nil || !strings.Contains(err.Error(), "each canonical storageDir must have exactly one owner") {
		t.Fatalf("ownership validation error = %v", err)
	}
}

func TestCanonicalStoragePathResolvesExistingSymlinkParent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	alias := filepath.Join(root, "alias")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	want := canonicalStoragePath(filepath.Join(target, "not-created", "control"))
	if got := canonicalStoragePath(filepath.Join(alias, "not-created", "control")); got != want {
		t.Fatalf("canonical storage path = %q, want %q", got, want)
	}
}
