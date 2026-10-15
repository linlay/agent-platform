// TODO(connector-compat): 下个版本随旧 OAuth/grant 适配删除本兼容测试文件。
package connectorauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
)

func TestLegacyOAuthSettingsAndCLIGrant(t *testing.T) {
	for _, mode := range []string{"oauth", "mcp"} {
		t.Run(mode, func(t *testing.T) {
			settings := map[string]any{
				"client_id": "default-client", "authorization_endpoint": "https://auth.test/authorize",
				"token_endpoint": "https://auth.test/token", "resource": "https://example.test/default",
				"redirect_uri": "http://127.0.0.1:42341/callback",
			}
			override := map[string]any{
				"client_id": "component-client", "authorization_endpoint": "https://auth.test/authorize",
				"token_endpoint": "https://auth.test/token", "resource": "https://example.test/selected",
			}
			authMode := "oauth"
			if mode == "mcp" {
				// Previous OAuth discovery manifests used auth_mode oauth.
				settings = map[string]any{"discovery": true, "client_metadata_url": "https://example.test/client.json"}
				override = map[string]any{"client_id": "component-client"}
			}
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
			if err != nil || string(pkg.AuthMode) != mode {
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
