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
		"env/connectors/demo/connector.json":         "ignored legacy package",
	})
	archive, err := extractArchive(source, "1.0.0", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".state", "connector-state", "connectors", "connectors-center"} {
		if _, err := os.Stat(filepath.Join(archive.extractedRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("archive imported private state %s: %v", name, err)
		}
	}
}

func TestSyncInstallsCurrentConnectorWithoutMigratingLegacyData(t *testing.T) {
	root := t.TempDir()
	untouched := map[string]string{
		"connectors/demo/connector.json":          "invalid legacy package",
		"connector-state/.credentials/demo.json":  "legacy credentials",
		".state/connectors/demo/credentials.json": "current credentials",
	}
	for relative, data := range untouched {
		writeTestFile(t, filepath.Join(root, relative), data)
	}
	manifest := `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":"none"}`
	source := writeTestZip(t, map[string]string{
		"env/VERSION": "1.0.0",
		"env/connectors-center/demo/connector.json": manifest,
		"env/connectors-center/demo/cli.json":       `{}`,
		"env/connectors/obsolete/connector.json":    "ignored legacy package",
	})
	options := Options{RuntimeDir: root, Source: source, DesktopFrom: "legacy", DesktopTo: "1.0.0", Mode: ModeVersionChange}
	if result, err := Sync(options); err != nil || !result.Changed {
		t.Fatalf("install: %+v %v", result, err)
	}
	installed := filepath.Join(root, "connectors-center", "demo")
	assertTestFile(t, filepath.Join(installed, "connector.json"), manifest)
	assertMissing(t, filepath.Join(root, "connectors-center", "obsolete"))
	if err := os.RemoveAll(installed); err != nil {
		t.Fatal(err)
	}
	// Reinstalling the same Desktop version does not force resource sync.
	if result, err := Sync(options); err != nil || result.Changed {
		t.Fatalf("same version: %+v %v", result, err)
	}
	assertMissing(t, installed)
	options.Mode = ModeManualImport
	if result, err := Sync(options); err != nil || !result.Changed {
		t.Fatalf("manual reimport: %+v %v", result, err)
	}
	assertTestFile(t, filepath.Join(installed, "connector.json"), manifest)
	for relative, data := range untouched {
		assertTestFile(t, filepath.Join(root, relative), data)
	}
}
