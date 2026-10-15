// TODO(connector-compat): 下个版本随 manifest_compat.go 删除本兼容测试文件。
package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyManifestAuthentication(t *testing.T) {
	for _, tc := range []struct {
		fields string
		mode   AuthMode
	}{
		{``, AuthDelegated},
		{`,"auth_mode":null`, AuthDelegated},
		{`,"auth_mode":"none"`, AuthDelegated},
		{`,"auth_mode":"cli"`, AuthDelegated},
		{`,"auth_mode":"no_auth"`, AuthNoAuth},
		{`,"auth_mode":"token"`, AuthToken},
		{`,"auth_mode":"mcp"`, AuthMCP},
		{`,"auth_mode":"oneid-token"`, AuthOneID},
		{`,"auth_mode":"oauth","oauth":{"discovery":true}`, AuthMCP},
		{`,"auth_mode":"oauth","oauth":{"clientId":"demo"}`, AuthOAuth},
		{`,"authMode":"none"`, AuthNoAuth},
	} {
		t.Run(tc.fields, func(t *testing.T) {
			var manifest Manifest
			if err := DecodeJSON([]byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp"`+tc.fields+`}`), &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.AuthMode != tc.mode {
				t.Fatalf("mode=%q, want %q", manifest.AuthMode, tc.mode)
			}
			encoded, err := json.Marshal(manifest)
			if err != nil || strings.Contains(string(encoded), "auth_mode") || !strings.Contains(string(encoded), `"authMode":"`+string(tc.mode)+`"`) {
				t.Fatalf("noncanonical manifest: %s (%v)", encoded, err)
			}
		})
	}
}

func writeLegacyPackage(t *testing.T, root string, files map[string]string) Package {
	t.Helper()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := Load(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func legacyTokenFiles() map[string]string {
	return map[string]string{
		"connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"token","auth_browser":"embedded",` +
			`"token_schema":{"title":"Credentials","fields":[{"name":"API_KEY","required":true}]},` +
			`"auth_bindings":{"cli":{"env":{"SERVICE_TOKEN":"${API_KEY}"}},"mcp:Remote":{"headers":{"X-API-Key":"${API_KEY}"}},"mcp:Local":{"env":{"SERVICE_TOKEN":"${API_KEY}"}}}}`,
		"cli.json": `{}`,
		"mcp.json": `{"mcpServers":{"Remote":{"type":"http","url":"https://example.test/mcp"},"Local":{"type":"stdio","command":"demo"}}}`,
	}
}

func TestLegacyTokenPackageImportSaveAndRuntimeCopy(t *testing.T) {
	root := t.TempDir()
	files := legacyTokenFiles()
	archive := archiveFixture(t, files)
	pkg, err := ImportArchive(context.Background(), Sources{ExternalRoot: root}, bytes.NewReader(archive), int64(len(archive)), false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.AuthMode != AuthToken || pkg.AuthorizationBrowser() != "embedded" || pkg.MCP["Remote"]["type"] != "streamableHttp" {
		t.Fatalf("legacy settings not normalized: %+v", pkg.Manifest)
	}
	fields, err := TokenFields(pkg.Manifest)
	if err != nil || len(fields) != 1 || fields[0].Key != "API_KEY" {
		t.Fatalf("inline schema: %+v (%v)", fields, err)
	}
	if err := ValidateTokenValues(pkg.Manifest, map[string]string{"API_KEY": "test-value"}); err != nil {
		t.Fatal(err)
	}
	public, err := pkg.CLIConfigEnvironment()
	if err != nil || len(public) != 0 {
		t.Fatalf("credentials exposed as public env: %v (%v)", public, err)
	}
	private, err := pkg.CLICredentialTemplates()
	if err != nil || private["SERVICE_TOKEN"] != "${API_KEY}" {
		t.Fatalf("CLI credential binding lost: %v (%v)", private, err)
	}
	if pkg.MCP["Remote"]["headers"].(map[string]any)["X-API-Key"] != "${API_KEY}" || pkg.MCP["Local"]["env"].(map[string]any)["SERVICE_TOKEN"] != "${API_KEY}" {
		t.Fatal("MCP credential bindings lost")
	}
	for name, want := range files {
		content, err := os.ReadFile(filepath.Join(root, "demo", name))
		if err != nil || string(content) != want {
			t.Fatalf("source %s changed: %s (%v)", name, content, err)
		}
	}
	previous, err := ReadFile(root, "demo", "connector.json")
	if err != nil {
		t.Fatal(err)
	}
	input := previous
	input.Content = strings.Replace(input.Content, `"name":"Demo"`, `"name":"Updated"`, 1)
	if _, err := SaveDefinition(root, input, previous.SHA256, nil, nil); err != nil {
		t.Fatal(err)
	}
	copyDir := filepath.Join(t.TempDir(), "runtime-copy")
	if err := copyPackage(filepath.Join(root, "demo"), copyDir); err != nil {
		t.Fatal(err)
	}
	runtimePkg, err := LoadDirectory(copyDir, "demo")
	if err != nil || runtimePkg.Name != "Updated" || runtimePkg.CLI["env"].(map[string]any)["SERVICE_TOKEN"] != "${API_KEY}" {
		t.Fatalf("runtime copy lost compatibility: %+v (%v)", runtimePkg, err)
	}
	summaries, err := (Sources{ExternalRoot: root}).Summaries()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(summaries)
	if err != nil || !strings.Contains(string(encoded), `"tokenSchema":`) || strings.Contains(string(encoded), `auth_bindings`) || strings.Contains(string(encoded), `token_schema`) {
		t.Fatalf("noncanonical summary: %s (%v)", encoded, err)
	}
}

func TestLegacyPackageOmittedModeAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		mcp  string
		mode AuthMode
	}{
		{`{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`, AuthDelegated},
		{`{"mcpServers":{"main":{"type":"http","url":"https://example.test/mcp","platform":{"authSource":"identity-file"}}}}`, AuthOneID},
	} {
		pkg := writeLegacyPackage(t, t.TempDir(), map[string]string{
			"connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp"}`,
			"mcp.json":       tc.mcp,
		})
		if pkg.AuthMode != tc.mode {
			t.Fatalf("mode=%s, want %s", pkg.AuthMode, tc.mode)
		}
	}
}

func TestManifestCompatibilityRejectsAmbiguousAndInvalidFields(t *testing.T) {
	for _, fields := range []string{
		`"auth_mode":"none","authMode":"none"`,
		`"auth_mode":"cli","auth_browser":"system","authBrowser":"embedded"`,
		`"auth_mode":"token","token_schema":{},"tokenSchema":{}`,
		`"auth_mode":"token","token_schema":{"fields":[{"key":"KEY","name":"OTHER"}]}`,
		`"auth_mode":"token","token_schema":{"fields":[{"name":"KEY","unknown":true}]}`,
		`"auth_mode":"token","token_schema":{"fields":[{"name":"KEY","name":"OTHER"}]}`,
		`"auth_mode":"token","token_schema":null`,
		`"auth_mode":"none","auth_bindings":{"cli":{"unknown":true}}`,
		`"auth_mode":"none","unknown":true`,
		`"auth_mode":"none","auth_mode":"no_auth"`,
		`"auth_mode":false`, `"auth_mode":""`, `"auth_mode":"server-side"`,
	} {
		if err := ValidateManifest("demo", []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp",`+fields+`}`)); err == nil {
			t.Fatalf("accepted invalid compatibility input: %s", fields)
		}
	}
}

func TestLegacyPackageRejectsInvalidBindingsAndSchemaConflicts(t *testing.T) {
	for _, tc := range []struct{ target, field, key, value string }{
		{"cli", "env", "SERVICE_TOKEN", "${MISSING}"},
		{"cli", "env", "SERVICE_TOKEN", "inline-secret"},
		{"cli", "env", "BASH_ENV", "${API_KEY}"},
		{"cli", "env", "AP_TOKEN", "${API_KEY}"},
		{"cli", "env", "APP_CONFIG_DIR", "${API_KEY}"},
		{"mcp:Remote", "env", "SERVICE_TOKEN", "${API_KEY}"},
		{"mcp:Local", "headers", "X-Key", "${API_KEY}"},
		{"mcp:Missing", "headers", "X-Key", "${API_KEY}"},
		{"mcp:Remote", "headers", "Host", "${API_KEY}"},
	} {
		t.Run(tc.target+tc.key+tc.value, func(t *testing.T) {
			root := t.TempDir()
			files := legacyTokenFiles()
			var manifest map[string]any
			if err := json.Unmarshal([]byte(files["connector.json"]), &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["auth_bindings"] = map[string]any{tc.target: map[string]any{tc.field: map[string]string{tc.key: tc.value}}}
			encoded, _ := json.Marshal(manifest)
			files["connector.json"] = string(encoded)
			archive := archiveFixture(t, files)
			if _, err := ImportArchive(context.Background(), Sources{ExternalRoot: root}, bytes.NewReader(archive), int64(len(archive)), false, nil, nil); err == nil {
				t.Fatal("accepted invalid credential binding")
			}
		})
	}
	for _, extra := range []map[string]string{
		{"token-schema.json": `{"fields":[{"key":"API_KEY"}]}`},
		{"cli.json": `{"env":{"SERVICE_TOKEN":"existing"}}`},
		{"mcp.json": `{"mcpServers":{"Remote":{"type":"streamableHttp","url":"https://example.test/mcp","headers":{"x-api-key":"existing"}},"Local":{"type":"stdio","command":"demo"}}}`},
	} {
		files := legacyTokenFiles()
		for name, content := range extra {
			files[name] = content
		}
		archive := archiveFixture(t, files)
		if _, err := ImportArchive(context.Background(), Sources{ExternalRoot: t.TempDir()}, bytes.NewReader(archive), int64(len(archive)), false, nil, nil); err == nil {
			t.Fatal("accepted conflicting legacy and current declarations")
		}
	}
}
