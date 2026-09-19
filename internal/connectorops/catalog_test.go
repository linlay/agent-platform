package connectorops

import (
	"agent-platform/internal/connector"
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogDoesNotLoadBusinessMapping(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "operations.json"), []byte("invalid ignored legacy file"), 0600)
	c, err := Load(connector.Package{Manifest: connector.Manifest{ID: "wecom"}, Dir: dir, CLI: map[string]any{}})
	if err != nil || len(c.Adapters) != 1 || c.Adapters[0] != "cli" {
		t.Fatal(c, err)
	}
}
func TestRequestAdaptersCannotBeMixed(t *testing.T) {
	for _, r := range []Request{{ConnectorID: "demo", Adapter: "cli", Args: []string{"x"}, ToolName: "x"}, {ConnectorID: "demo", Adapter: "mcp", Args: []string{"x"}, Component: "main", ToolName: "x", Arguments: map[string]any{}}, {ConnectorID: "demo", Adapter: "cli", Args: []string{"a\x00b"}}} {
		if validateRequest(r) == nil {
			t.Fatal("accepted", r)
		}
	}
}
