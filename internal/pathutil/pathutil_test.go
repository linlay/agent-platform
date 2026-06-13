package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCanonicalizeCleansRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	path := filepath.Join(root, "dir", "file.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Chdir(root)

	canonical, err := Canonicalize(filepath.Join(".", "dir", "..", "dir", "file.txt"))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if canonical.Host != realPathForTest(t, path) {
		t.Fatalf("Host = %q, want %q", canonical.Host, realPathForTest(t, path))
	}
	if strings.Contains(canonical.Posix, "\\") {
		t.Fatalf("expected POSIX path, got %q", canonical.Posix)
	}
}

func TestCanonicalizeResolvesSymlinkAncestorForMissingSuffix(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	canonical, err := Canonicalize(filepath.Join(link, "missing", "new.txt"))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	want := filepath.Join(realPathForTest(t, outside), "missing", "new.txt")
	if canonical.Host != want {
		t.Fatalf("Host = %q, want %q", canonical.Host, want)
	}

	rootCanonical, err := Canonicalize(root)
	if err != nil {
		t.Fatalf("canonicalize root: %v", err)
	}
	if WithinRoot(canonical, rootCanonical) {
		t.Fatalf("expected symlink escape %q to be outside %q", canonical.Host, rootCanonical.Host)
	}
}

func TestWithinRootUsesPathComponentBoundaries(t *testing.T) {
	root := Canonical{Key: "/proj"}
	if !WithinRoot(Canonical{Key: "/proj"}, root) {
		t.Fatal("expected root to contain itself")
	}
	if !WithinRoot(Canonical{Key: "/proj/sub"}, root) {
		t.Fatal("expected child to be inside root")
	}
	if WithinRoot(Canonical{Key: "/proj-evil"}, root) {
		t.Fatal("expected sibling prefix to be outside root")
	}
	if !WithinRoot(Canonical{Key: "/anything"}, Canonical{Key: "/"}) {
		t.Fatal("expected filesystem root to contain absolute path")
	}
	if !WithinRoot(Canonical{Key: "c:/users/u/proj"}, Canonical{Key: "c:/"}) {
		t.Fatal("expected drive root to contain drive child")
	}
}

func TestCanonicalKeyUsesPlatformCaseSensitivity(t *testing.T) {
	root := t.TempDir()
	upper, err := Canonicalize(filepath.Join(root, "A.txt"))
	if err != nil {
		t.Fatalf("canonicalize upper: %v", err)
	}
	lower, err := Canonicalize(filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatalf("canonicalize lower: %v", err)
	}
	if caseInsensitive {
		if upper.Key != lower.Key {
			t.Fatalf("expected case-insensitive keys to match: %q != %q", upper.Key, lower.Key)
		}
		return
	}
	if upper.Key == lower.Key {
		t.Fatalf("expected case-sensitive keys to differ: %q", upper.Key)
	}
}

func TestCanonicalKeyNormalizesDarwinUnicode(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("NFC key normalization is Darwin-only")
	}
	root := t.TempDir()
	nfc, err := Canonicalize(filepath.Join(root, "\u00e9.txt"))
	if err != nil {
		t.Fatalf("canonicalize NFC: %v", err)
	}
	nfd, err := Canonicalize(filepath.Join(root, "e\u0301.txt"))
	if err != nil {
		t.Fatalf("canonicalize NFD: %v", err)
	}
	if nfc.Key != nfd.Key {
		t.Fatalf("expected NFC and NFD keys to match: %q != %q", nfc.Key, nfd.Key)
	}
}

func TestCanonicalizeWindowsForms(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only path forms")
	}
	upper, err := Canonicalize(`C:\Users\u\proj\a.py`)
	if err != nil {
		t.Fatalf("canonicalize upper drive: %v", err)
	}
	lower, err := Canonicalize(`c:/users/u/proj/a.py`)
	if err != nil {
		t.Fatalf("canonicalize lower drive: %v", err)
	}
	if upper.Key != lower.Key {
		t.Fatalf("expected drive case and slash forms to match: %q != %q", upper.Key, lower.Key)
	}
	if !strings.Contains(upper.Posix, "/") || strings.Contains(upper.Posix, "\\") {
		t.Fatalf("expected POSIX slash form, got %q", upper.Posix)
	}
	if root, err := Canonicalize(`C:\`); err != nil {
		t.Fatalf("canonicalize drive root: %v", err)
	} else if !WithinRoot(upper, root) {
		t.Fatalf("expected %q to be within %q", upper.Key, root.Key)
	}
	_, _ = Canonicalize(`\\server\share\dir`)
}

func realPathForTest(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return real
}
