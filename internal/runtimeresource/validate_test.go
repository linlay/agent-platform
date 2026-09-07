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
	if _, err := os.Stat(filepath.Join(root, "connectors", "invalid")); !os.IsNotExist(err) {
		t.Fatal("legacy source was imported as a connector")
	}
}
