package builtins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureProcessPathFindsServiceRootBin(t *testing.T) {
	root := t.TempDir()
	backendDir := filepath.Join(root, "backend")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(backendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "system-bin"))
	clearProcessBinDir(t)

	got, err := configureProcessPathForExecutable(filepath.Join(backendDir, "agent-platform"))
	if err != nil {
		t.Fatalf("configureProcessPathForExecutable: %v", err)
	}
	if got != binDir {
		t.Fatalf("bin dir = %s, want %s", got, binDir)
	}
	if first := filepath.SplitList(os.Getenv("PATH"))[0]; first != binDir {
		t.Fatalf("PATH first = %s, want %s", first, binDir)
	}
}

func TestConfigureProcessPathPrefersExplicitBuildCache(t *testing.T) {
	root := t.TempDir()
	backendDir := filepath.Join(root, "backend")
	releaseBin := filepath.Join(root, "bin")
	cacheBin := filepath.Join(root, "build", "builtins", "darwin-arm64", "bin")
	for _, directory := range []string{backendDir, releaseBin, cacheBin} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(processBinDirEnv, cacheBin)
	t.Setenv("PATH", filepath.Join(root, "system-bin"))
	clearProcessBinDir(t)

	got, err := configureProcessPathForExecutable(filepath.Join(backendDir, "agent-platform"))
	if err != nil {
		t.Fatalf("configureProcessPathForExecutable: %v", err)
	}
	if got != cacheBin {
		t.Fatalf("bin dir = %s, want explicit cache %s", got, cacheBin)
	}
	if first := filepath.SplitList(os.Getenv("PATH"))[0]; first != cacheBin {
		t.Fatalf("PATH first = %s, want %s", first, cacheBin)
	}
}

func TestEnsureBinInEnvKeepsTrustedBinFirst(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	processBinState.Lock()
	processBinState.dir = binDir
	processBinState.Unlock()
	t.Cleanup(func() { clearProcessBinDir(t) })

	env := EnsureBinInEnv([]string{"HOME=/tmp", "PATH=/system/bin", "PATH=/agent/bin"})
	pathEntries := []string{}
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			pathEntries = filepath.SplitList(strings.TrimPrefix(item, "PATH="))
		}
	}
	if len(pathEntries) != 2 || pathEntries[0] != binDir || pathEntries[1] != "/agent/bin" {
		t.Fatalf("PATH entries = %#v", pathEntries)
	}
}

func clearProcessBinDir(t *testing.T) {
	t.Helper()
	processBinState.Lock()
	processBinState.dir = ""
	processBinState.Unlock()
}
