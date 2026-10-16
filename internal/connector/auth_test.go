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
		{`"mcp"`, AuthMCP}, {`"oauth"`, AuthMCP}, {`"oneid"`, AuthOneID}, {`"oneid-token"`, AuthOneID},
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

func TestStandaloneOAuthSettingsAreRejectedExplicitly(t *testing.T) {
	for _, tc := range []struct{ extra, want string }{
		{`"authMode":"oauth","oauth":{"clientId":"demo","authorizationEndpoint":"https://idp.test/authorize"}`, "oauth.authorizationEndpoint is no longer supported"},
		{`"authMode":"mcp","oauth":{"tokenEndpoint":"https://idp.test/token"}`, "oauth.tokenEndpoint is no longer supported"},
		{`"authMode":"mcp","oauth":{"discovery":true}`, "oauth.discovery is not supported"},
		{`"authMode":"token","oauth":{"clientId":"demo"}`, "oauth settings require authMode mcp, but authMode is token"},
		{`"authMode":"server-side"`, `authMode "server-side" is not supported`},
	} {
		err := ValidateManifest("demo", []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp",`+tc.extra+`}`))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %v, want %q", tc.extra, err, tc.want)
		}
	}
	if err := ValidateManifest("demo", []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","authMode":"oauth","oauth":{"resourceMetadataUrl":"https://mcp.test/.well-known/oauth-protected-resource","scopes":["read"]}}`)); err != nil {
		t.Fatal(err)
	}
}

// TODO(connector-compat): 下个版本恢复为拒绝旧字段的测试。
func TestLegacyManifestFieldsAreAcceptedForThisRelease(t *testing.T) {
	for _, fields := range []string{
		`"auth_mode":"none"`,
		`"authMode":"self","auth_browser":"system"`,
		`"authMode":"token","token_schema":{"fields":[{"key":"TOKEN"}]}`,
		`"authMode":"token","tokenSchema":{"fields":[{"key":"TOKEN"}]}`,
		`"authMode":"self","auth_bindings":{"cli":{"env":{"TOKEN":"x"}}}`,
	} {
		if err := ValidateManifest("demo", []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp",`+fields+`}`)); err != nil {
			t.Fatal("rejected legacy field", fields, err)
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
	for _, tc := range []struct{ mode, schema, want string }{
		{"token", "", "authMode token requires token-schema.json"},
		{"none", valid, "token-schema.json is only allowed with authMode token, but authMode is none"},
		{"self", valid, "token-schema.json is only allowed with authMode token, but authMode is self"},
		{"token", `{"fields":[]}`, "fields must contain at least one field"},
		{"token", `{"fields":[{"key":"TOKEN"},{"key":"TOKEN"}]}`, `fields[1].key "TOKEN" is duplicated`},
		{"token", `{"fields":[{"key":"token"}]}`, `fields[0].key "token" must be an uppercase identifier`},
		{"token", `{"fields":[{"name":"TOKEN"}]}`, `unknown field "name"`},
		{"token", `{"fields":[{"key":"TOKEN","type":"select"}]}`, `field TOKEN type "select" must be text or password`},
		{"token", `{"fields":[{"key":"TOKEN","type":"password","defaultValue":"sk-secret"}]}`, "field TOKEN is a password and cannot declare defaultValue"},
		{"token", `{"title_en":"Legacy","fields":[{"key":"TOKEN"}]}`, "translations belong in i18n.<locale>"},
		{"token", `{"fields":[{"key":"TOKEN","label_en":"Token"}]}`, `unknown field "label_en"`},
	} {
		err := writeTokenSchemaPackage(t, tc.mode, tc.schema)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s with %q: error %v, want %q", tc.mode, tc.schema, err, tc.want)
		}
	}
}
