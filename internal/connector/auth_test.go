package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthModeValuesAreExplicit(t *testing.T) {
	for _, tc := range []struct {
		auth string
		want AuthMode
	}{
		{`"none"`, AuthNoAuth}, {`"self"`, AuthDelegated}, {`"token"`, AuthToken},
		{`"mcp"`, AuthMCP}, {`"oneid"`, AuthOneID}, {`"oneid-token"`, AuthOneID},
	} {
		data := []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","authMode":` + tc.auth + `}`)
		if err := ValidateManifest("demo", data); err != nil {
			t.Fatal(tc.auth, err)
		}
		var manifest Manifest
		if err := DecodeJSON(data, &manifest); err != nil || manifest.AuthMode != tc.want {
			t.Fatalf("%s: mode=%q want %q (%v)", tc.auth, manifest.AuthMode, tc.want, err)
		}
		encoded, _ := json.Marshal(manifest)
		if !strings.Contains(string(encoded), `"authMode":"`+string(tc.want)+`"`) {
			t.Fatalf("not canonical: %s", encoded)
		}
	}
	for _, auth := range []string{`,"authMode":null`, ``, `,"authMode":""`, `,"authMode":"no_auth"`, `,"authMode":"cli"`, `,"authMode":"server-side"`, `,"authMode":true`} {
		if err := ValidateManifest("demo", []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp"`+auth+`}`)); err == nil {
			t.Fatal("accepted invalid mode", auth)
		}
	}
}

func TestLegacyManifestFieldsAreRejected(t *testing.T) {
	for _, fields := range []string{
		`"auth_mode":"none"`,
		`"authMode":"none","auth_browser":"system"`,
		`"authMode":"token","token_schema":{"fields":[{"key":"TOKEN"}]}`,
		`"authMode":"token","tokenSchema":{"fields":[{"key":"TOKEN"}]}`,
		`"authMode":"self","auth_bindings":{"cli":{"env":{"TOKEN":"x"}}}`,
	} {
		if err := ValidateManifest("demo", []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp",`+fields+`}`)); err == nil {
			t.Fatal("accepted legacy field", fields)
		}
	}
	if err := ValidateManifest("demo", []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","authMode":"self","oauth":{"clientId":"demo"}}`)); err == nil {
		t.Fatal("accepted oauth settings for another mode")
	}
}

func writeTokenSchemaPackage(t *testing.T, mode, schema string) error {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "connector.json"), []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","authMode":"`+mode+`"}`), 0600)
	os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`), 0600)
	if schema != "" {
		os.WriteFile(filepath.Join(dir, "token-schema.json"), []byte(schema), 0600)
	}
	_, err := Load(root, "demo")
	return err
}

func TestTokenSchemaFile(t *testing.T) {
	valid := `{"title":"配置凭证","docLabel":"如何获取？","docUrl":"https://example.test/doc","i18n":{"en":{"title":"Configure"}},` +
		`"fields":[{"key":"API_KEY","label":"API Key","type":"password","required":true,"placeholder":"sk-","i18n":{"en":{"label":"API Key"}}},` +
		`{"key":"REGION","label":"Region","type":"text","required":false,"defaultValue":"cn"}]}`
	if err := writeTokenSchemaPackage(t, "token", valid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ mode, schema string }{
		{"token", ""},
		{"none", valid},
		{"self", valid},
		{"token", `{"fields":[]}`},
		{"token", `{"fields":[{"key":"TOKEN"},{"key":"TOKEN"}]}`},
		{"token", `{"fields":[{"key":"token"}]}`},
		{"token", `{"fields":[{"name":"TOKEN"}]}`},
		{"token", `{"fields":[{"key":"TOKEN","type":"password","defaultValue":"sk-secret"}]}`},
		{"token", `{"title_en":"Legacy","fields":[{"key":"TOKEN"}]}`},
	} {
		if err := writeTokenSchemaPackage(t, tc.mode, tc.schema); err == nil {
			t.Fatalf("accepted %s with %q", tc.mode, tc.schema)
		}
	}
}
