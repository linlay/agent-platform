package connectorauth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
)

func TestWindowsBatchInitAndGlobalCommandWithSpaces(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin with spaces")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+";"+os.Getenv("PATH"))
	cli := simpleCLI()
	marker := filepath.Join(t.TempDir(), "initialized")
	cli["staticEnv"] = map[string]any{"TEST_MARKER": marker}
	cli["init"] = osCommands("@echo off\r\nif not exist connector.json exit /b 9\r\necho @echo off>\"%CONNECTOR_BIN_DIR%\\demo.cmd\"\r\necho echo 1.2.0>>\"%CONNECTOR_BIN_DIR%\\demo.cmd\"\r\necho done>\"%TEST_MARKER%\"\r\n")
	writeCLIPackage(t, root, "demo", cli)
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	if _, err := m.Prepare(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("multiline init did not finish", err)
	}
}
