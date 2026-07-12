package builtins

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveProcessBuiltinUsesConfiguredBundleBinOnly(t *testing.T) {
	root := t.TempDir()
	bundleBin := filepath.Join(root, "bundle", "bin")
	pathBin := filepath.Join(root, "path-bin")
	if err := os.MkdirAll(bundleBin, 0o755); err != nil {
		t.Fatalf("mkdir bundle bin: %v", err)
	}
	if err := os.MkdirAll(pathBin, 0o755); err != nil {
		t.Fatalf("mkdir PATH bin: %v", err)
	}
	filename := "kbase-lance-engine"
	if runtime.GOOS == "windows" {
		filename += ".exe"
	}
	bundleExecutable := filepath.Join(bundleBin, filename)
	if err := os.WriteFile(bundleExecutable, []byte("bundle"), 0o755); err != nil {
		t.Fatalf("write bundle executable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pathBin, filename), []byte("path"), 0o755); err != nil {
		t.Fatalf("write PATH executable: %v", err)
	}
	t.Setenv("PATH", pathBin)
	setProcessBinDirForTest(t, bundleBin)

	got, err := ResolveProcessBuiltin("kbase-lance-engine")
	if err != nil {
		t.Fatalf("ResolveProcessBuiltin: %v", err)
	}
	if got != bundleExecutable {
		t.Fatalf("resolved path = %q, want trusted bundle %q", got, bundleExecutable)
	}

	if err := os.Remove(bundleExecutable); err != nil {
		t.Fatalf("remove bundle executable: %v", err)
	}
	if _, err := ResolveProcessBuiltin("kbase-lance-engine"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing bundle error = %v, want os.ErrNotExist despite PATH copy", err)
	}
}

func TestResolveProcessBuiltinValidatesNameAndFileType(t *testing.T) {
	binDir := t.TempDir()
	setProcessBinDirForTest(t, binDir)
	for _, name := range []string{"", "../kbase-lance-engine", filepath.Join("nested", "kbase-lance-engine")} {
		if _, err := ResolveProcessBuiltin(name); err == nil {
			t.Fatalf("ResolveProcessBuiltin(%q) succeeded, want invalid name error", name)
		}
	}

	directoryName := "directory-helper"
	if runtime.GOOS == "windows" {
		directoryName += ".exe"
	}
	if err := os.Mkdir(filepath.Join(binDir, directoryName), 0o755); err != nil {
		t.Fatalf("mkdir fake helper: %v", err)
	}
	if _, err := ResolveProcessBuiltin("directory-helper"); err == nil {
		t.Fatal("ResolveProcessBuiltin accepted a directory")
	}
}

func TestResolveProcessBuiltinWithoutConfiguredBinDoesNotSearchPATH(t *testing.T) {
	pathBin := t.TempDir()
	filename := "kbase-lance-engine"
	if runtime.GOOS == "windows" {
		filename += ".exe"
	}
	if err := os.WriteFile(filepath.Join(pathBin, filename), []byte("path"), 0o755); err != nil {
		t.Fatalf("write PATH executable: %v", err)
	}
	t.Setenv("PATH", pathBin)
	clearProcessBinDir(t)
	if _, err := ResolveProcessBuiltin("kbase-lance-engine"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ResolveProcessBuiltin error = %v, want os.ErrNotExist", err)
	}
}

func setProcessBinDirForTest(t *testing.T, dir string) {
	t.Helper()
	processBinState.Lock()
	processBinState.dir = dir
	processBinState.Unlock()
	t.Cleanup(func() { clearProcessBinDir(t) })
}
