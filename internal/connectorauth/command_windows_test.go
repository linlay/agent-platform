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
	t.Setenv("TEST_CLI_BIN", bin)
	cli := simpleCLI()
	cli["init"] = osCommands("@echo off\r\nif not exist connector.json exit /b 9\r\necho @echo off>\"%TEST_CLI_BIN%\\demo.cmd\"\r\necho echo 1.2.0>>\"%TEST_CLI_BIN%\\demo.cmd\"\r\necho done>initialized\r\n")
	pkg := writeCLIPackage(t, root, "demo", cli)
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	if _, err := m.Prepare(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pkg.Dir, "initialized")); err != nil {
		t.Fatal("multiline init did not finish", err)
	}
}
