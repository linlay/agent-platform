package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func TestPreAuthCLIStartsMCPOnlyAfterCLILogin(t *testing.T) {
	sources := connector.Sources{ExternalRoot: filepath.Join(t.TempDir(), "connectors")}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := map[string]any{"darwin": "exit 0", "linux": "exit 0", "win32": "exit 0"}
	for name, value := range map[string]any{
		"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": "self"},
		"cli.json":       map[string]any{"auth": ok, "status": ok, "unAuth": ok},
		"mcp.json":       map[string]any{"preAuth": "cli", "mcpServers": map[string]any{"main": map[string]any{"type": "stdio", "command": "demo-mcp"}}},
	} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	before, err := connectorServer(pkg, "main")
	if err != nil || before.SetupError == "" {
		t.Fatal("MCP component started before CLI login", before.SetupError, err)
	}
	m := connectorauth.New(t.Context(), sources, nil)
	if _, err := m.Prepare(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	if checked, err := m.Check(t.Context(), "demo", ""); err != nil || checked.Status != "authorized" {
		t.Fatal(checked, err)
	}
	after, err := connectorServer(pkg, "main")
	if err != nil || after.SetupError != "" || after.CredentialRevision == before.CredentialRevision {
		t.Fatal("CLI login did not release the MCP component", after.SetupError, err)
	}
}
