package supportpkg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDirLoadsMatchingSupportPackageExecutable(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "test-support-package")
	binaryPath := filepath.Join(pluginDir, "payload", "windows-amd64", "bin", "helper.exe")
	mustWriteFile(t, binaryPath, "fake exe")
	mustWriteFile(t, filepath.Join(pluginDir, ManifestName), `{
  "kind": "support-package",
  "id": "test-support-package",
  "version": "v0.3.9",
  "platform": { "os": "windows", "arch": "amd64" },
  "executables": {
    "helper": "payload/windows-amd64/bin/helper.exe"
  }
}`)

	registry, errs := LoadDir(root, Target{OS: "windows", Arch: "amd64"})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	executable, ok := registry.Executable("helper.exe")
	if !ok {
		t.Fatal("expected helper executable")
	}
	if executable.Path != binaryPath {
		t.Fatalf("unexpected executable path: got %q want %q", executable.Path, binaryPath)
	}
	if executable.PluginID != "test-support-package" || executable.Version != "v0.3.9" {
		t.Fatalf("unexpected executable metadata: %#v", executable)
	}
	executables := registry.Executables()
	if len(executables) != 1 || executables[0].Name != "helper" {
		t.Fatalf("unexpected executable list: %#v", executables)
	}
}

func TestCandidatePluginRootsUsesOnlyBundleRootWhenExecutableIsInBackend(t *testing.T) {
	executable := filepath.Join("Users", "me", "Library", "Application Support", "BrandApp", "services", "agent-platform", "v0.3.12", "backend", "agent-platform")
	roots := CandidatePluginRoots(executable)
	want := []string{
		filepath.Join("Users", "me", "Library", "Application Support", "BrandApp", "services", "agent-platform", "v0.3.12", PluginsDir),
	}
	if len(roots) != len(want) {
		t.Fatalf("unexpected roots: got %#v want %#v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Fatalf("unexpected root[%d]: got %q want %q", i, roots[i], want[i])
		}
	}
}

func TestLoadDirsUsesEarlierRootForDuplicateExecutable(t *testing.T) {
	root := t.TempDir()
	servicePluginDir := filepath.Join(root, "agent-platform", "plugins", "test-support-package")
	backendPluginDir := filepath.Join(root, "agent-platform", "backend", "plugins", "test-support-package")
	serviceBinaryPath := filepath.Join(servicePluginDir, "bin", "helper")
	backendBinaryPath := filepath.Join(backendPluginDir, "bin", "helper")
	writeSupportManifest(t, servicePluginDir, "darwin", "arm64", "bin/helper")
	writeSupportManifest(t, backendPluginDir, "darwin", "arm64", "bin/helper")
	mustWriteFile(t, serviceBinaryPath, "service")
	mustWriteFile(t, backendBinaryPath, "backend")

	registry, errs := LoadDirs([]string{
		filepath.Join(root, "agent-platform", "plugins"),
		filepath.Join(root, "agent-platform", "backend", "plugins"),
	}, Target{OS: "darwin", Arch: "arm64"})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	executable, ok := registry.Executable("helper")
	if !ok {
		t.Fatal("expected helper executable")
	}
	if executable.Path != serviceBinaryPath {
		t.Fatalf("unexpected executable path: got %q want %q", executable.Path, serviceBinaryPath)
	}
}

func TestLoadDirSkipsNonMatchingPlatform(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "test-support-package")
	mustWriteFile(t, filepath.Join(pluginDir, "pdftotext.exe"), "fake exe")
	mustWriteFile(t, filepath.Join(pluginDir, ManifestName), `{
  "kind": "support-package",
  "id": "test-support-package",
  "platform": { "os": "linux", "arch": "amd64" },
  "executables": { "pdftotext": "pdftotext.exe" }
}`)

	registry, errs := LoadDir(root, Target{OS: "windows", Arch: "amd64"})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if _, ok := registry.Executable("pdftotext"); ok {
		t.Fatal("did not expect executable for non-matching platform")
	}
}

func TestLoadDirSkipsMissingExecutables(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "test-support-package")
	mustWriteFile(t, filepath.Join(pluginDir, ManifestName), `{
  "kind": "support-package",
  "id": "test-support-package",
  "platform": { "os": "windows", "arch": "amd64" }
}`)

	registry, errs := LoadDir(root, Target{OS: "windows", Arch: "amd64"})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if _, ok := registry.Executable("pdftotext"); ok {
		t.Fatal("did not expect executable when executables is missing")
	}
}

func TestLoadDirSkipsMissingExecutableTarget(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "test-support-package")
	mustWriteFile(t, filepath.Join(pluginDir, ManifestName), `{
  "kind": "support-package",
  "id": "test-support-package",
  "platform": { "os": "windows", "arch": "amd64" },
  "executables": { "pdftotext": "missing.exe" }
}`)

	registry, errs := LoadDir(root, Target{OS: "windows", Arch: "amd64"})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if _, ok := registry.Executable("pdftotext"); ok {
		t.Fatal("did not expect executable when target file is missing")
	}
}

func TestLoadDirIgnoresUnsupportedKind(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "other")
	mustWriteFile(t, filepath.Join(pluginDir, ManifestName), `{
  "kind": "external-tool",
  "id": "other",
  "platform": { "os": "windows", "arch": "amd64" },
  "executables": { "pdftotext": "pdftotext.exe" }
}`)

	registry, errs := LoadDir(root, Target{OS: "windows", Arch: "amd64"})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if registry.ExecutableCount() != 0 {
		t.Fatalf("expected no executables, got %d", registry.ExecutableCount())
	}
}

func TestLoadDirIgnoresRetiredPDFExtractor(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, retiredPDFExtractorID)
	mustWriteFile(t, filepath.Join(pluginDir, "pdftotext"), "legacy")
	mustWriteFile(t, filepath.Join(pluginDir, ManifestName), `{
  "kind": "support-package",
  "id": "pdf-extractor",
  "version": "v0.3.9",
  "platform": { "os": "darwin", "arch": "arm64" },
  "executables": { "pdftotext": "pdftotext" }
}`)

	registry, errs := LoadDir(root, Target{OS: "darwin", Arch: "arm64"})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if registry.ExecutableCount() != 0 || len(registry.Packages()) != 0 {
		t.Fatalf("retired PDF plugin must not be discovered: %#v %#v", registry.Packages(), registry.Executables())
	}
}

func writeSupportManifest(t *testing.T, pluginDir string, goos string, goarch string, executablePath string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(pluginDir, ManifestName), `{
  "kind": "support-package",
  "id": "test-support-package",
  "version": "v0.3.9",
  "platform": { "os": "`+goos+`", "arch": "`+goarch+`" },
  "executables": {
    "helper": "`+executablePath+`"
  }
}`)
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
