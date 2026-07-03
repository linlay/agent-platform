package supportpkg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDirLoadsMatchingSupportPackageExecutable(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "pdf-extractor")
	binaryPath := filepath.Join(pluginDir, "payload", "windows-amd64", "Library", "bin", "pdftotext.exe")
	mustWriteFile(t, binaryPath, "fake exe")
	mustWriteFile(t, filepath.Join(pluginDir, ManifestName), `{
  "kind": "support-package",
  "id": "pdf-extractor",
  "version": "v0.3.9",
  "platform": { "os": "windows", "arch": "amd64" },
  "executables": {
    "pdftotext": "payload/windows-amd64/Library/bin/pdftotext.exe"
  }
}`)

	registry, errs := LoadDir(root, Target{OS: "windows", Arch: "amd64"})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	executable, ok := registry.Executable("pdftotext.exe")
	if !ok {
		t.Fatal("expected pdftotext executable")
	}
	if executable.Path != binaryPath {
		t.Fatalf("unexpected executable path: got %q want %q", executable.Path, binaryPath)
	}
	if executable.PluginID != "pdf-extractor" || executable.Version != "v0.3.9" {
		t.Fatalf("unexpected executable metadata: %#v", executable)
	}
	executables := registry.Executables()
	if len(executables) != 1 || executables[0].Name != "pdftotext" {
		t.Fatalf("unexpected executable list: %#v", executables)
	}
}

func TestLoadDirSkipsNonMatchingPlatform(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "pdf-extractor")
	mustWriteFile(t, filepath.Join(pluginDir, "pdftotext.exe"), "fake exe")
	mustWriteFile(t, filepath.Join(pluginDir, ManifestName), `{
  "kind": "support-package",
  "id": "pdf-extractor",
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
	pluginDir := filepath.Join(root, "pdf-extractor")
	mustWriteFile(t, filepath.Join(pluginDir, ManifestName), `{
  "kind": "support-package",
  "id": "pdf-extractor",
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
	pluginDir := filepath.Join(root, "pdf-extractor")
	mustWriteFile(t, filepath.Join(pluginDir, ManifestName), `{
  "kind": "support-package",
  "id": "pdf-extractor",
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

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
