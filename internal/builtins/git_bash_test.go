package builtins

import (
	"os"
	"path/filepath"
	"testing"
)

func gitBashFixture(t *testing.T) (string, Manifest) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"usr/bin/bash.exe", "usr/bin/cygpath.exe", "usr/bin/msys-2.0.dll", "mingw64/bin/git.exe", "etc/package-versions.txt", "LICENSE.txt"} {
		mustWrite(t, filepath.Join(root, GitBashRelativeRoot, filepath.FromSlash(name)), []byte(name))
	}
	tree := []TreeOutput{{Path: GitBashRelativeRoot, Type: "dir"}}
	hash, err := TreeDigest(root, tree)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{SchemaVersion: 1, Platform: ManifestPlatform{OS: "windows", Arch: "amd64"}, Components: []ManifestComponent{{Name: GitBashComponent, Version: "v1.0.0", Path: GitBashRelativeRoot, Tree: tree, SHA256: hash}}}
	writeCacheManifest(t, filepath.Join(root, "builtins.manifest.json"), m)
	return root, m
}

func TestGitBashCompleteVerifiedTree(t *testing.T) {
	root, m := gitBashFixture(t)
	if _, err := verifyGitBashAt(root); err != nil {
		t.Fatal(err)
	}
	if err := RequirePlatformComponents(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, GitBashRelativeRoot, "usr/bin/bash.exe"), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyGitBashAt(root); err == nil {
		t.Fatal("tampered DLL/runtime accepted")
	}
}

func TestGitBashReleaseRequiredIndependentlyOfConfiguration(t *testing.T) {
	cache := newSingleFileCache(t, "windows", "amd64")
	if _, err := StageCache(CacheStageOptions{CacheDir: cache, OutputDir: t.TempDir(), GOOS: "windows", GOARCH: "amd64"}); err == nil {
		t.Fatal("Windows release without Git Bash accepted")
	}
	if err := RequirePlatformComponents(Manifest{Platform: ManifestPlatform{OS: "linux", Arch: "amd64"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveGitBash("windows", "arm64"); err == nil {
		t.Fatal("unsupported managed target accepted")
	}
}
