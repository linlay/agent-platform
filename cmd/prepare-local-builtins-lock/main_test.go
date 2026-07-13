package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/builtins"
)

func TestRunAddsLocalSidecarTargetAndRefreshesHashes(t *testing.T) {
	root := t.TempDir()
	collection := filepath.Join(root, "collection")
	archive := filepath.Join(collection, "kbase-lance-engine", "dist", "v1.0.0", "kbase-lance-engine_v1.0.0_linux_amd64.tar.gz")
	writeArchive(t, archive)

	lock := builtins.Lock{
		SchemaVersion: 1,
		DefaultRoot:   "../agent-platform-builtins",
		Components: []builtins.Component{{
			Name: "kbase-lance-engine", Version: "1.0.0", Repository: "kbase-lance-engine", Kind: "archive", Required: true,
			Targets: map[string]builtins.Target{
				"darwin-arm64": {
					Path: "dist/v1.0.0/kbase-lance-engine_v1.0.0_darwin_arm64.tar.gz", Format: "tar.gz", Entry: "kbase-lance-engine", Output: "kbase-lance-engine", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Metadata: &builtins.TargetMetadata{CargoMetadata: "cargo-metadata.json", SBOM: "sbom.cdx.json"},
				},
			},
		}},
	}
	input := filepath.Join(root, "lock.json")
	payload, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "local-lock.json")
	if err := run(input, output, collection, []string{"linux/amd64"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	derived, err := builtins.LoadLock(output)
	if err != nil {
		t.Fatalf("load derived lock: %v", err)
	}
	target, ok := derived.Components[0].Targets["linux-amd64"]
	if !ok {
		t.Fatal("derived lock did not add linux-amd64")
	}
	if target.Path != "dist/v1.0.0/kbase-lance-engine_v1.0.0_linux_amd64.tar.gz" {
		t.Fatalf("path = %q", target.Path)
	}
	if target.Metadata == nil || target.Metadata.CargoMetadata != "cargo-metadata.json" || target.Metadata.SBOM != "sbom.cdx.json" {
		t.Fatalf("metadata = %#v", target.Metadata)
	}
	hash, err := fileSHA256(archive)
	if err != nil {
		t.Fatal(err)
	}
	if target.SHA256 != hash {
		t.Fatalf("sha256 = %s, want %s", target.SHA256, hash)
	}
}

func writeArchive(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	gzipWriter := gzip.NewWriter(&payload)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range map[string]string{
		"kbase-lance-engine":  "binary",
		"cargo-metadata.json": `{}`,
		"sbom.cdx.json":       `{}`,
	} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
