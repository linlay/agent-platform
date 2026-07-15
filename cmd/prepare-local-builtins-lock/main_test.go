package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/builtins"
)

func TestRunAddsLocalSidecarTargetAndRefreshesHashes(t *testing.T) {
	root := t.TempDir()
	collection := filepath.Join(root, "collection")
	archive := filepath.Join(collection, "kbase-lance-engine", "dist", "v1.0.0", "kbase-lance-engine_v1.0.0_linux_amd64.tar.gz")
	writeArchive(t, archive)
	if err := os.WriteFile(filepath.Join(collection, "kbase-lance-engine", "VERSION"), []byte("v1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

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
	if derived.Components[0].Version != "v1.0.0" {
		t.Fatalf("version = %q", derived.Components[0].Version)
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

func TestRunRefreshesArchiveAndArchiveTreeHashesWithoutChangingCanonicalLock(t *testing.T) {
	root := t.TempDir()
	collection := filepath.Join(root, "collection")
	archivePayload := []byte("rebuilt archive")
	archivePath := filepath.Join(collection, "dbx", "dist", "v1", "dbx_v1_darwin_arm64.tar.gz")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, archivePayload, 0o644); err != nil {
		t.Fatal(err)
	}
	treeArchive := filepath.Join(collection, "poppler-pdftotext", "dist", "v1", "poppler-pdftotext_v1_darwin_arm64.tar.gz")
	writeArchive(t, treeArchive)

	lock := builtins.Lock{
		SchemaVersion: 1,
		DefaultRoot:   "../agent-platform-builtins",
		Components: []builtins.Component{
			{
				Name: "dbx", Version: "v1", Repository: "dbx", Kind: "archive", Required: true,
				Targets: map[string]builtins.Target{
					"darwin-arm64": {Path: "dist/v1/dbx_v1_darwin_arm64.tar.gz", Format: "tar.gz", Entry: "dbx", Output: "dbx", SHA256: strings.Repeat("a", 64)},
				},
			},
			{
				Name: "poppler-pdftotext", Version: "v1", Repository: "poppler-pdftotext", Kind: "archive-tree", Required: false,
				Targets: map[string]builtins.Target{
					"darwin-arm64": {
						Path: "dist/v1/poppler-pdftotext_v1_darwin_arm64.tar.gz", Format: "tar.gz", SHA256: strings.Repeat("b", 64),
						Tree: &builtins.TreeLayout{Root: "runtime", Outputs: []builtins.TreeOutput{
							{Path: "bin/pdftotext", Type: "file"},
							{Path: "libexec/poppler-pdftotext/darwin-arm64", Type: "dir"},
						}},
					},
				},
			},
			{
				Name: "optional", Version: "v1", Repository: "optional", Kind: "file", Required: false,
				Targets: map[string]builtins.Target{
					"windows-amd64": {Path: "optional.exe", Output: "optional.exe", SHA256: strings.Repeat("c", 64)},
				},
			},
		},
	}
	input := filepath.Join(root, "lock.json")
	payload, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	canonical, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "local-lock.json")
	if err := run(input, output, collection, []string{"darwin/arm64"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	after, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, canonical) {
		t.Fatal("run changed the canonical lock")
	}
	derived, err := builtins.LoadLock(output)
	if err != nil {
		t.Fatal(err)
	}
	dbx, err := builtins.FindComponent(derived, "dbx")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dbx.Targets["darwin-arm64"].SHA256, hashBytes(archivePayload); got != want {
		t.Fatalf("dbx sha256 = %s, want %s", got, want)
	}
	poppler, err := builtins.FindComponent(derived, "poppler-pdftotext")
	if err != nil {
		t.Fatal(err)
	}
	treeHash, err := fileSHA256(treeArchive)
	if err != nil {
		t.Fatal(err)
	}
	if got := poppler.Targets["darwin-arm64"].SHA256; got != treeHash {
		t.Fatalf("poppler sha256 = %s, want %s", got, treeHash)
	}
	optional, err := builtins.FindComponent(derived, "optional")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := optional.Targets["darwin-arm64"]; ok {
		t.Fatal("optional component unexpectedly gained an unsupported target")
	}
}

func TestDeclaredComponentTargetsReturnsOnlyLockedRequestedTargets(t *testing.T) {
	root := t.TempDir()
	lock := builtins.Lock{
		SchemaVersion: 1,
		DefaultRoot:   "../agent-platform-builtins",
		Components: []builtins.Component{{
			Name: "poppler-pdftotext", Version: "v1", Repository: "poppler-pdftotext", Kind: "archive-tree", Required: false,
			Targets: map[string]builtins.Target{
				"darwin-arm64": {
					Path: "dist/poppler.tar.gz", Format: "tar.gz", SHA256: strings.Repeat("d", 64),
					Tree: &builtins.TreeLayout{Root: "runtime", Outputs: []builtins.TreeOutput{{Path: "bin/pdftotext", Type: "file"}}},
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

	got, err := declaredComponentTargets(input, "poppler-pdftotext", []string{"linux/amd64", "darwin/arm64", "darwin/arm64", "windows/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"darwin/arm64"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %#v, want %#v", got, want)
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

func hashBytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
