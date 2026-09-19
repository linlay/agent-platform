package connectorops

import (
	"agent-platform/internal/connector"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOperationManifestFreezesPackageAndHidesExecution(t *testing.T) {
	dir := t.TempDir()
	pkg := connector.Package{Manifest: connector.Manifest{ID: "demo"}, Dir: dir, MCP: map[string]map[string]any{"main": {}}}
	data := `{"version":1,"operations":[{"operationId":"read","description":"Read","effect":"read","adapter":"mcp","mcp":{"component":"main","tool":"private_tool"},"inputSchema":{"type":"object","properties":{"count":{"type":"integer"}},"additionalProperties":false},"outputSchema":{"type":"object"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "operations.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(pkg)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(catalog)
	var response map[string]any
	json.Unmarshal(public, &response)
	op := response["operations"].([]any)[0].(map[string]any)
	if op["mcp"] != nil || op["adapter"] != nil {
		t.Fatal("execution mapping leaked")
	}
	if err := validateValue(catalog.Operations[0].input, []byte(`{"count":2}`)); err != nil {
		t.Fatal("integer JSON rejected", err)
	}
	if catalog.Operations[0].input.Validate(map[string]any{"shell": "rm"}) == nil {
		t.Fatal("unknown argument accepted")
	}
	os.WriteFile(filepath.Join(dir, "dependency"), []byte("changed"), 0600)
	next, err := Load(pkg)
	if err != nil || next.Revision == catalog.Revision {
		t.Fatal("package change did not change revision", err)
	}
}
func TestNoRemoteSchemaAndStrictOutput(t *testing.T) {
	if _, err := resolveSchema(json.RawMessage(`{"type":"object","$ref":"https://example.test/schema"}`)); err == nil {
		t.Fatal("remote schema accepted")
	}
	for _, value := range []string{`[]`, `null`, `{} {}`, `not json`} {
		if _, err := decodeOutput([]byte(value)); err == nil {
			t.Fatal("invalid output accepted", value)
		}
	}
	output, err := decodeOutput([]byte(`{"id":"12345678901234567890","count":2}`))
	if err != nil || output["id"] != "12345678901234567890" {
		t.Fatal(output, err)
	}
}
func TestEntryRejectsShellAndEscape(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "bin"), 0700)
	os.WriteFile(filepath.Join(dir, "bin", "run"), []byte("#!/bin/sh\necho hello"), 0700)
	for _, entry := range []string{"bin/run", "bin/../run", "/bin/sh", "bin\\run"} {
		if _, err := Entry(connector.Package{Dir: dir}, entry); err == nil {
			t.Fatal("unsafe entry", entry)
		}
	}
}
