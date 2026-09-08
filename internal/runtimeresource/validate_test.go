package runtimeresource

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateCandidateIgnoresLegacyMCPRegistry(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "registries", "mcp-servers", "invalid.yml")
	content := "serverKey: [broken YAML deliberately ignored\n"
	writeTestFile(t, legacy, content)
	if err := validateCandidate(root); err != nil {
		t.Fatalf("legacy registry blocked validation: %v", err)
	}
	data, err := os.ReadFile(legacy)
	if err != nil || string(data) != content {
		t.Fatalf("validation changed ignored source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "connectors-center", "invalid")); !os.IsNotExist(err) {
		t.Fatal("legacy source was imported as a connector")
	}
}

func TestResourceArchiveExcludesPlatformState(t *testing.T) {
	source := writeTestZip(t, map[string]string{
		"env/VERSION":                                "1.0.0",
		"env/.state/connectors/demo/oauth.json":      "private",
		"env/.state/other/session.json":              "other-state",
		"env/connector-state/.credentials/demo.json": "legacy-private",
	})
	archive, err := extractArchive(source, "1.0.0", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".state", "connector-state"} {
		if _, err := os.Stat(filepath.Join(archive.extractedRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("archive imported private state %s: %v", name, err)
		}
	}
}
