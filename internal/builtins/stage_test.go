package builtins

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageBuiltins(t *testing.T) {
	base := t.TempDir()
	repoRoot := filepath.Join(base, "agent-platform")
	collectionRoot := filepath.Join(base, "agent-platform-builtins")
	mustMkdirAll(t, repoRoot)

	rgPath := filepath.Join(collectionRoot, "ripgrep", "15.1.0", "darwin-arm64", "rg")
	mustWrite(t, rgPath, []byte("rg-binary"))
	mustWrite(t, filepath.Join(collectionRoot, "ripgrep", "15.1.0", "LICENSE-MIT"), []byte("license"))

	dbxArchive := filepath.Join(collectionRoot, "dbx", "dist", "v0.1.0", "dbx.tar.gz")
	mustWriteTarGzip(t, dbxArchive, "./dbx", []byte("dbx-binary"))
	httpxArchive := filepath.Join(collectionRoot, "httpx", "dist", "v0.1.1", "httpx.zip")
	mustWriteZip(t, httpxArchive, "httpx", []byte("httpx-binary"))

	lock := Lock{
		SchemaVersion: lockSchemaVersion,
		DefaultRoot:   "../agent-platform-builtins",
		Components: []Component{
			{
				Name: "rg", Version: "15.1.0", Repository: "ripgrep", Kind: "file", Required: true,
				LicenseDirectory: "ripgrep",
				Licenses:         []string{"15.1.0/LICENSE-MIT"},
				Targets: map[string]Target{
					"darwin-arm64": {Path: "15.1.0/darwin-arm64/rg", Output: "rg", SHA256: fileSHA256(t, rgPath)},
				},
			},
			{
				Name: "dbx", Version: "v0.1.0", Repository: "dbx", Kind: "archive", Required: true,
				Targets: map[string]Target{
					"darwin-arm64": {Path: "dist/v0.1.0/dbx.tar.gz", Format: "tar.gz", Entry: "dbx", Output: "dbx", SHA256: fileSHA256(t, dbxArchive)},
				},
			},
			{
				Name: "httpx", Version: "v0.1.1", Repository: "httpx", Kind: "archive", Required: true,
				Targets: map[string]Target{
					"darwin-arm64": {Path: "dist/v0.1.1/httpx.zip", Format: "zip", Entry: "httpx", Output: "httpx", SHA256: fileSHA256(t, httpxArchive)},
				},
			},
		},
	}
	lockPath := filepath.Join(repoRoot, "builtins.lock.json")
	writeLock(t, lockPath, lock)
	outputDir := filepath.Join(base, "release-local")

	result, err := Stage(StageOptions{
		RepoRoot:  repoRoot,
		LockPath:  lockPath,
		OutputDir: outputDir,
		GOOS:      "darwin",
		GOARCH:    "arm64",
	})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if result.BuiltinsRoot != collectionRoot {
		t.Fatalf("builtins root = %s, want %s", result.BuiltinsRoot, collectionRoot)
	}
	for name, expected := range map[string]string{
		"rg":    "rg-binary",
		"dbx":   "dbx-binary",
		"httpx": "httpx-binary",
	} {
		payload, err := os.ReadFile(filepath.Join(outputDir, "bin", name))
		if err != nil {
			t.Fatalf("read staged %s: %v", name, err)
		}
		if string(payload) != expected {
			t.Fatalf("staged %s = %q, want %q", name, payload, expected)
		}
	}
	if _, err := os.Stat(filepath.Join(outputDir, "licenses", "ripgrep", "LICENSE-MIT")); err != nil {
		t.Fatalf("staged license: %v", err)
	}
	if len(result.Manifest.Components) != 3 {
		t.Fatalf("manifest components = %d, want 3", len(result.Manifest.Components))
	}
	if _, err := os.Stat(result.ManifestPath); err != nil {
		t.Fatalf("manifest path: %v", err)
	}
}

func TestStageRejectsChecksumMismatch(t *testing.T) {
	base := t.TempDir()
	repoRoot := filepath.Join(base, "agent-platform")
	collectionRoot := filepath.Join(base, "agent-platform-builtins")
	mustMkdirAll(t, repoRoot)
	mustWrite(t, filepath.Join(collectionRoot, "ripgrep", "rg"), []byte("rg"))
	lock := Lock{
		SchemaVersion: lockSchemaVersion,
		DefaultRoot:   "../agent-platform-builtins",
		Components: []Component{{
			Name: "rg", Version: "1", Repository: "ripgrep", Kind: "file", Required: true,
			Targets: map[string]Target{
				"linux-amd64": {Path: "rg", Output: "rg", SHA256: strings.Repeat("0", 64)},
			},
		}},
	}
	lockPath := filepath.Join(repoRoot, "builtins.lock.json")
	writeLock(t, lockPath, lock)

	_, err := Stage(StageOptions{
		RepoRoot:  repoRoot,
		LockPath:  lockPath,
		OutputDir: filepath.Join(base, "output"),
		GOOS:      "linux",
		GOARCH:    "amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestStageRegistersButDefersSourceBuildComponent(t *testing.T) {
	base := t.TempDir()
	repoRoot := filepath.Join(base, "agent-platform")
	mustMkdirAll(t, repoRoot)
	lock := Lock{
		SchemaVersion: lockSchemaVersion,
		DefaultRoot:   "builtins",
		Components: []Component{{
			Name:         "kbase-lance-engine",
			Version:      "1.0.0",
			Repository:   ".",
			Source:       "native/kbase-lance-engine",
			Kind:         "file",
			Required:     true,
			Distribution: "source-build",
			BuildPath:    "native/kbase-lance-engine",
			BuildTargets: map[string]string{"linux-amd64": "kbase-lance-engine"},
			SDKVersion:   "lancedb=0.30.0",
			License:      "Apache-2.0",
		}},
	}
	lockPath := filepath.Join(repoRoot, "builtins.lock.json")
	writeLock(t, lockPath, lock)
	outputDir := filepath.Join(base, "output")

	result, err := Stage(StageOptions{
		RepoRoot:  repoRoot,
		LockPath:  lockPath,
		OutputDir: outputDir,
		GOOS:      "linux",
		GOARCH:    "amd64",
	})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(result.Manifest.Components) != 0 {
		t.Fatalf("components = %#v, want deferred source-build component", result.Manifest.Components)
	}
	if _, err := os.Stat(result.ManifestPath); err != nil {
		t.Fatalf("manifest path: %v", err)
	}
}

func writeLock(t *testing.T, path string, lock Lock) {
	t.Helper()
	payload, err := json.Marshal(lock)
	if err != nil {
		t.Fatalf("marshal lock: %v", err)
	}
	mustWrite(t, path, payload)
}

func mustWriteTarGzip(t *testing.T, path string, name string, payload []byte) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(payload))}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tarWriter.Write(payload); err != nil {
		t.Fatalf("tar payload: %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	mustWrite(t, path, archive.Bytes())
}

func mustWriteZip(t *testing.T, path string, name string, payload []byte) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create(name)
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := entry.Write(payload); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	mustWrite(t, path, archive.Bytes())
}

func mustWrite(t *testing.T, path string, payload []byte) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return bytesSHA256(payload)
}
