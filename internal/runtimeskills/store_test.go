package runtimeskills

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDigestSurvivesSealingAndTracksDependencies(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skill")
	t.Cleanup(func() { _ = Remove(root) })
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "scripts", "dependency.py")
	if err := os.WriteFile(file, []byte("value = 1"), 0610); err != nil {
		t.Fatal(err)
	}
	before, err := Digest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Seal(root); err != nil {
		t.Fatal(err)
	}
	after, err := Digest(root)
	if err != nil || after != before {
		t.Fatal("sealing changed content identity", err)
	}
	if err := os.Chmod(file, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("value = 2"), 0700); err != nil {
		t.Fatal(err)
	}
	changed, err := Digest(root)
	if err != nil || changed == before {
		t.Fatal("non-entry dependency change was not detected", err)
	}
}

func TestDigestRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlinks require host privileges")
	}
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := Digest(root); err == nil {
		t.Fatal("symlink accepted into immutable snapshot")
	}
}

func TestResolveRejectsLegacyTreeWithoutReferences(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills", "old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "old", "SKILL.md"), []byte("old layout"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(root, "old"); err == nil {
		t.Fatal("missing references fell back to old layout")
	}
}
