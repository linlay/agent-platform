package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/builtins"
)

func TestRunStagesVerifiedExternalReleaseMetadata(t *testing.T) {
	repoRoot := t.TempDir()
	collectionRoot := filepath.Join(repoRoot, "builtins")
	outputDir := filepath.Join(repoRoot, "bundle")
	artifact := filepath.Join(collectionRoot, componentName, "dist", "v1.0.0", "sidecar.tar.gz")
	cargoMetadata := []byte(`{"packages":[{"name":"lancedb","version":"0.30.0","license":"Apache-2.0","source":"registry+https://token@github.com/rust-lang/crates.io-index?signature=secret","manifest_path":"/secret/build/path"}]}`)
	mustWriteTarGzip(t, artifact, map[string][]byte{
		componentName:         []byte("sidecar-binary"),
		"cargo-metadata.json": cargoMetadata,
		"sbom.cdx.json":       []byte(`{"bomFormat":"CycloneDX"}`),
	})
	mustWrite(t, filepath.Join(collectionRoot, componentName, "LICENSE-APACHE-2.0"), []byte("license"), 0o644)
	mustWrite(t, filepath.Join(collectionRoot, componentName, "NOTICE"), []byte("notice"), 0o644)

	lock := builtins.Lock{
		SchemaVersion: 1,
		DefaultRoot:   "builtins",
		Components: []builtins.Component{{
			Name:             componentName,
			Version:          "1.0.0",
			Repository:       componentName,
			Source:           "agent-platform-builtins/kbase-lance-engine",
			Kind:             "archive",
			Required:         true,
			SDKVersion:       "lancedb=0.30.0",
			License:          "Apache-2.0",
			LicenseDirectory: componentName,
			Licenses:         []string{"LICENSE-APACHE-2.0", "NOTICE"},
			Targets: map[string]builtins.Target{
				"linux-amd64": {
					Path:   "dist/v1.0.0/sidecar.tar.gz",
					Format: "tar.gz",
					Entry:  componentName,
					Output: componentName,
					SHA256: fileSHA256(t, artifact),
					Metadata: &builtins.TargetMetadata{
						CargoMetadata: "cargo-metadata.json",
						SBOM:          "sbom.cdx.json",
					},
				},
			},
		}},
	}
	mustWriteJSON(t, filepath.Join(repoRoot, "builtins.lock.json"), lock)
	if _, err := builtins.Stage(builtins.StageOptions{
		RepoRoot:  repoRoot,
		LockPath:  "builtins.lock.json",
		OutputDir: outputDir,
		GOOS:      "linux",
		GOARCH:    "amd64",
	}); err != nil {
		t.Fatalf("generic stage: %v", err)
	}

	if err := run(repoRoot, "builtins.lock.json", "", outputDir, "linux", "amd64"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "bin", componentName)); err != nil {
		t.Fatalf("staged binary: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "licenses", componentName, "LICENSE-APACHE-2.0")); err != nil {
		t.Fatalf("staged license: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "sbom", "kbase-lance-engine.cdx.json")); err != nil {
		t.Fatalf("staged SBOM: %v", err)
	}
	inventory, err := os.ReadFile(filepath.Join(outputDir, "licenses", componentName, "THIRD_PARTY_COMPONENTS.json"))
	if err != nil {
		t.Fatalf("dependency inventory: %v", err)
	}
	if strings.Contains(string(inventory), "/secret/build/path") || strings.Contains(string(inventory), "token") || strings.Contains(string(inventory), "signature") {
		t.Fatalf("dependency inventory leaked sensitive build metadata: %s", inventory)
	}
	payload, err := os.ReadFile(filepath.Join(outputDir, "builtins.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest builtins.Manifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Components) != 1 || manifest.Components[0].SDKVersion != "lancedb=0.30.0" {
		t.Fatalf("manifest components = %#v", manifest.Components)
	}
	if manifest.Components[0].Distribution != "checksum-verified-artifact" {
		t.Fatalf("distribution = %q", manifest.Components[0].Distribution)
	}

	if err := os.WriteFile(artifact, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(repoRoot, "builtins.lock.json", "", outputDir, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("tampered archive error = %v", err)
	}
}

func mustWriteTarGzip(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, payload := range entries {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(payload))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, archive.Bytes(), 0o644)
}

func mustWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, payload, 0o644)
}

func mustWrite(t *testing.T, path string, payload []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, mode); err != nil {
		t.Fatal(err)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmtSHA256(payload)
}

func fmtSHA256(payload []byte) string {
	// Delegate the digest formatting to the staging package's invariant via a
	// small local hash rather than shelling out in this unit test.
	hash := sha256.Sum256(payload)
	return fmt.Sprintf("%x", hash[:])
}
