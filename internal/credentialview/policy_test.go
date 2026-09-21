package credentialview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourcesAndContent(t *testing.T) {
	root := t.TempDir()
	p := Policy{Providers: filepath.Join(root, "registries", "providers"), Connectors: filepath.Join(root, ".state", "connectors"), IdentityFile: filepath.Join(root, "custom-token")}
	for _, tc := range []struct {
		file, input, visible string
		forbidden            []string
	}{
		{filepath.Join(p.Providers, "demo.yml"), "key: demo\nbaseUrl: https://example.test\napiKey: secret-value\n", "baseUrl: https://example.test", []string{"secret-value"}},
		{filepath.Join(p.Providers, "demo.yml"), "api-key: |\n  multiline-secret\nname: Demo\n", "name: Demo", []string{"multiline-secret"}},
		{filepath.Join(p.Connectors, "demo", "oauth.json"), `{"config":{"ClientID":"public-client","ClientSecret":"client-secret"},"token":{"access_token":"access-secret"},"status":"ready"}`, "ready", []string{"client-secret", "access-secret"}},
		{filepath.Join(p.Connectors, "demo", "credentials.json"), `{"CUSTOM_KEY":"arbitrary-secret"}`, "CUSTOM_KEY", []string{"arbitrary-secret"}},
		{p.IdentityFile, "opaque-identity", Hidden, []string{"opaque-identity"}},
	} {
		t.Run(filepath.Base(tc.file), func(t *testing.T) {
			got := p.Text(tc.file, tc.input, false)
			if !strings.Contains(got, tc.visible) {
				t.Fatal(got)
			}
			for _, v := range tc.forbidden {
				if strings.Contains(got, v) {
					t.Fatalf("secret leaked: %s", got)
				}
			}
			if p.Text(tc.file, got, false) != got {
				t.Fatalf("not idempotent: %s", got)
			}
		})
	}
	plain := "key: demo\nmodelConfig:\n  modelKey: sample\n"
	for _, name := range []string{"agents/demo/agent.yml", "teams/demo.yml", "skills/demo/SKILL.md", "connectors-center/demo/connector.json", "registries/providers-other/demo.yml"} {
		if got := p.Text(filepath.Join(root, name), plain, false); got != plain {
			t.Fatal(name, got)
		}
	}
	if got := p.Text(filepath.Join(p.Providers, "demo.yml"), "secret fragment", true); got != Hidden {
		t.Fatal(got)
	}
	if err := os.MkdirAll(p.Providers, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(p.Providers, link); err == nil {
		if p.Source(filepath.Join(link, "new.yml")) != Provider {
			t.Fatal("symlink source missed")
		}
	}
}
func TestWindowsSources(t *testing.T) {
	p := Policy{Providers: `C:\Users\Demo\registries\providers`, Connectors: `C:\Users\Demo\.state\connectors`, IdentityFile: `C:\Users\Demo\identity\access-token`}
	for _, tc := range []struct {
		path string
		want Source
	}{
		{`c:\users\DEMO\registries\providers\a.yml`, Provider}, {`C:/Users/Demo/.state/connectors/a/oauth.json`, ConnectorState}, {`C:\Users\Demo\identity\access-token`, Identity}, {`C:\Users\Demo\registries\providers-extra\a.yml`, Ordinary},
	} {
		if got := p.Source(tc.path); got != tc.want {
			t.Fatalf("%s: %v", tc.path, got)
		}
	}
}
func TestArgumentsAndGrep(t *testing.T) {
	p := Policy{Providers: "/runtime/registries/providers"}
	raw := `{"file_path":"/runtime/registries/providers/demo.yml","content":"key: demo\napiKey: secret-value\n"}`
	got := p.Arguments("file_write", raw)
	if strings.Contains(got, "secret-value") || !strings.Contains(got, "key: demo") {
		t.Fatal(got)
	}
	if !strings.Contains(raw, "secret-value") {
		t.Fatal("mutated execution input")
	}
	line := "/runtime/registries/providers/demo.yml:2:apiKey: secret-value\n/runtime/agents/demo/agent.yml:1:key: demo"
	got = p.Grep("/runtime", line)
	if strings.Contains(got, "secret-value") || !strings.Contains(got, "key: demo") {
		t.Fatal(got)
	}
}
