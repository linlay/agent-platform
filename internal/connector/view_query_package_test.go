package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeViewQueryPackage(t *testing.T, root string, files map[string]any) {
	t.Helper()
	dir := filepath.Join(root, "crm")
	if err := os.MkdirAll(filepath.Join(dir, "views"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range files {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "views", "orders.html"), []byte("<p></p>"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ordersView(query map[string]any) map[string]any {
	return map[string]any{"views": map[string]any{"orders": map[string]any{
		"description": "Orders.", "renderer": "html", "usage": []string{"display"}, "entry": "views/orders.html",
		"queries": map[string]any{"list": query},
	}}}
}

// External queries may use only the declaring package's own CLI or MCP
// components; native operations stay with their native connector.
func TestViewQueriesUseOnlyTheDeclaringPackagesComponents(t *testing.T) {
	manifest := map[string]any{"id": "crm", "name": "CRM", "version": "1.0.0", "type": "mcp", "authMode": "none"}
	mcpConfig := map[string]any{"mcpServers": map[string]any{"crmapi": map[string]any{"type": "streamableHttp", "url": "https://crm.example/mcp"}}}
	for name, tc := range map[string]struct {
		query map[string]any
		want  string
	}{
		"own mcp":       {map[string]any{"adapter": "mcp", "component": "crmapi", "toolName": "list_orders"}, ""},
		"foreign mcp":   {map[string]any{"adapter": "mcp", "component": "other", "toolName": "list_orders"}, "not declared in mcp.json"},
		"missing cli":   {map[string]any{"adapter": "cli", "args": []any{"orders"}}, "no cli.json"},
		"native borrow": {map[string]any{"adapter": "native", "operation": "automation.list"}, "not registered"},
	} {
		root := t.TempDir()
		writeViewQueryPackage(t, root, map[string]any{"connector.json": manifest, "mcp.json": mcpConfig, "view.json": ordersView(tc.query)})
		_, err := Load(root, "crm")
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
