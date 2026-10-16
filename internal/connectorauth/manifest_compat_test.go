// TODO(connector-compat): 下个版本随旧 OAuth/grant 适配删除本兼容测试文件。
package connectorauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/connector"
)

func TestLegacyOAuthSettingsAndCLIGrant(t *testing.T) {
	for _, mode := range []string{"discovery", "plain"} {
		t.Run(mode, func(t *testing.T) {
			// Previous manifests used auth_mode oauth; with or without the old
			// discovery flag it now always means MCP standard authorization.
			settings := map[string]any{"discovery": true, "client_metadata_url": "https://example.test/client.json"}
			if mode == "plain" {
				settings = map[string]any{"client_id": "default-client"}
			}
			override := map[string]any{"client_id": "component-client"}
			authMode := "oauth"
			root := t.TempDir()
			dir := filepath.Join(root, "demo")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]any{
				"connector.json": map[string]any{
					"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": authMode, "oauth": settings,
					"auth_bindings": map[string]any{
						"mcp:selected": map[string]any{"oauth": override},
						"cli":          map[string]any{"grant": "selected", "env": map[string]string{"SERVICE_TOKEN": "${ACCESS_TOKEN}"}},
					},
				},
				"cli.json": map[string]any{},
				"mcp.json": map[string]any{"mcpServers": map[string]any{
					"selected": map[string]any{"type": "streamableHttp", "url": "https://example.test/selected"},
					"other":    map[string]any{"type": "streamableHttp", "url": "https://example.test/other"},
				}},
			} {
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), encoded, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			pkg, err := connector.Load(root, "demo")
			if err != nil || pkg.AuthMode != connector.AuthMCP {
				t.Fatalf("legacy OAuth load: mode=%s, %v", pkg.AuthMode, err)
			}
			if err := ValidatePackage(pkg); err != nil {
				t.Fatal(err)
			}
			selected, err := OAuthComponent(pkg, "selected")
			if err != nil {
				t.Fatal(err)
			}
			resource, selectedSettings, err := oauthResource(selected)
			if err != nil || resource != "https://example.test/selected" || selectedSettings.ClientID != "component-client" {
				t.Fatalf("component override lost: %s, %+v (%v)", resource, selectedSettings, err)
			}
			env, err := CLIEnvironment(pkg)
			if err != nil || env.Resource != resource || env.Env["SERVICE_TOKEN"] != "${ACCESS_TOKEN}" {
				t.Fatalf("CLI grant lost: %+v (%v)", env, err)
			}
			public, err := pkg.CLIConfigEnvironment()
			if err != nil || len(public) != 0 {
				t.Fatalf("legacy credential became public: %v (%v)", public, err)
			}
			if len(pkg.MCP) != 2 {
				t.Fatal("component selection mutated source package")
			}
		})
	}
}

func TestLegacyStandaloneOAuthEndpointsAreRejected(t *testing.T) {
	for _, tc := range []struct{ manifest, want string }{
		{`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"oauth","oauth":{"client_id":"c","authorization_endpoint":"https://auth.test/authorize","token_endpoint":"https://auth.test/token"}}`, "oauth.authorizationEndpoint is no longer supported"},
		{`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"oauth","auth_bindings":{"mcp:main":{"oauth":{"token_endpoint":"https://auth.test/token"}}}}`, "auth_bindings.mcp:main oauth.tokenEndpoint is no longer supported"},
	} {
		root := t.TempDir()
		dir := filepath.Join(root, "demo")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "connector.json"), []byte(tc.manifest), 0o644)
		os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`), 0o644)
		if _, err := connector.Load(root, "demo"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("error %v, want %q", err, tc.want)
		}
	}
}
