package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/builtins"
)

func TestRunStagesVerifiedArtifactAndUpdatesManifest(t *testing.T) {
	repoRoot := t.TempDir()
	outputDir := filepath.Join(repoRoot, "bundle")
	artifact := filepath.Join(repoRoot, "artifact", componentName)
	mustWrite(t, artifact, []byte("sidecar-binary"), 0o755)
	actualSHA, err := fileSHA256(artifact)
	if err != nil {
		t.Fatal(err)
	}

	lock := builtins.Lock{
		SchemaVersion: 1,
		DefaultRoot:   "builtins",
		Components: []builtins.Component{{
			Name:         componentName,
			Version:      "1.0.0",
			Repository:   ".",
			Source:       "native/kbase-lance-engine",
			Kind:         "file",
			Required:     true,
			Distribution: "source-build",
			BuildPath:    "native/kbase-lance-engine",
			BuildTargets: map[string]string{"linux-amd64": componentName},
			SDKVersion:   "lancedb=0.30.0",
			License:      "Apache-2.0",
		}},
	}
	mustWriteJSON(t, filepath.Join(repoRoot, "builtins.lock.json"), lock)
	mustWriteJSON(t, filepath.Join(outputDir, "builtins.manifest.json"), builtins.Manifest{
		SchemaVersion: 1,
		Platform:      builtins.ManifestPlatform{OS: "linux", Arch: "amd64"},
	})
	mustWrite(t, filepath.Join(repoRoot, "scripts", "release-assets", "licenses", componentName, "NOTICE"), []byte("notice"), 0o644)
	mustWrite(t, filepath.Join(repoRoot, "scripts", "release-assets", "licenses", componentName, "LICENSE-APACHE-2.0"), []byte("license"), 0o644)
	cargoMetadata := filepath.Join(repoRoot, "cargo-metadata.json")
	mustWrite(t, cargoMetadata, []byte(`{"packages":[{"name":"lancedb","version":"0.30.0","license":"Apache-2.0","source":"registry+https://token@github.com/rust-lang/crates.io-index?signature=secret","manifest_path":"/secret/build/path"}]}`), 0o644)

	if err := run(repoRoot, "builtins.lock.json", outputDir, "linux", "amd64", artifact, actualSHA, "https://token@artifacts.example/engine?signature=secret", cargoMetadata, false); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "bin", componentName)); err != nil {
		t.Fatalf("staged binary: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "licenses", componentName, "LICENSE-APACHE-2.0")); err != nil {
		t.Fatalf("staged license: %v", err)
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
	if len(manifest.Components) != 1 || manifest.Components[0].SHA256 != actualSHA {
		t.Fatalf("manifest components = %#v", manifest.Components)
	}
	if manifest.Components[0].Distribution != "checksum-verified-artifact" {
		t.Fatalf("distribution = %q", manifest.Components[0].Distribution)
	}
	if manifest.Components[0].Source != "https://artifacts.example/engine" {
		t.Fatalf("source = %q", manifest.Components[0].Source)
	}
	if err := run(repoRoot, "builtins.lock.json", outputDir, "linux", "amd64", artifact, "", "", "", false); err == nil {
		t.Fatal("expected release artifact without checksum to fail")
	}
	if err := run(repoRoot, "builtins.lock.json", outputDir, "linux", "amd64", artifact, strings.Repeat("0", 64), "", cargoMetadata, true); err == nil {
		t.Fatal("expected local build with a mismatched checksum to fail")
	}
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
