package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func cliEnvPackage(t *testing.T, mode AuthMode, env map[string]any) Package {
	t.Helper()
	pkg := Package{Manifest: Manifest{ID: "demo", AuthMode: mode}, Dir: "/packages/demo", StateRoot: t.TempDir(), CLI: map[string]any{"env": env}}
	if mode == AuthToken {
		pkg.TokenSchema = json.RawMessage(`{"fields":[{"key":"API_KEY","required":true}]}`)
	}
	return pkg
}

func TestCLIEnvironmentValidation(t *testing.T) {
	for _, test := range []struct {
		name  string
		mode  AuthMode
		key   string
		value string
		valid bool
	}{
		{"literal", AuthDelegated, "REGION", "cn", true},
		{"config dir name", AuthDelegated, "FOO_CLI_CONFIG_DIR", "${CONNECTOR_CONFIG_DIR}", true},
		{"lowercase name", AuthDelegated, "npm_config_registry", "https://registry.npmmirror.com", true},
		{"empty literal", AuthDelegated, "OPTIONAL", "", true},
		{"token field", AuthToken, "FOO_KEY", "${API_KEY}", true},
		{"mcp token", AuthMCP, "FOO_TOKEN", "Bearer ${OAUTH_ACCESS_TOKEN}", true},
		{"oneid token", AuthOneID, "FOO_TOKEN", "${ONEID_TOKEN}", true},
		{"undeclared credential", AuthToken, "FOO_KEY", "${MISSING}", false},
		{"credential in self mode", AuthDelegated, "FOO_KEY", "${API_KEY}", false},
		{"old mcp name", AuthMCP, "FOO_TOKEN", "${ACCESS_TOKEN}", false},
		{"old oneid name", AuthOneID, "FOO_TOKEN", "${AP_ACCESS_TOKEN}", false},
		{"shell execution variable", AuthDelegated, "BASH_ENV", "x", false},
		{"path", AuthDelegated, "PATH", "/tmp", false},
		{"platform prefix", AuthDelegated, "AP_TOKEN", "x", false},
		{"platform variable name", AuthDelegated, "CONNECTOR_DIR", "x", false},
		{"multiline", AuthDelegated, "REGION", "a\nb", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := cliEnvPackage(t, test.mode, map[string]any{test.key: test.value}).cliEnvironment()
			if (err == nil) != test.valid {
				t.Fatalf("validation=%v, want valid=%v", err, test.valid)
			}
		})
	}
	if _, err := (Package{Manifest: Manifest{AuthMode: AuthDelegated}, CLI: map[string]any{"env": map[string]any{"N": 1}}}).cliEnvironment(); err == nil {
		t.Fatal("accepted a non-string value")
	}
}

func TestCLIEnvironmentSeparatesPublicValuesFromCredentials(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	pkg := cliEnvPackage(t, AuthToken, map[string]any{
		"FOO_REGION":     "cn",
		"FOO_HOME":       "$HOME/.foo",
		"FOO_CACHE":      "${HOME}/.cache/foo",
		"FOO_SCRIPT":     "${CONNECTOR_DIR}/bin/run.sh",
		"FOO_CONFIG_DIR": "${CONNECTOR_CONFIG_DIR}",
		"FOO_HOMEPAGE":   "$HOMEPAGE",
		"FOO_KEY":        "key=${API_KEY}",
	})
	public, err := pkg.CLIConfigEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	state, _ := StateDir(pkg.PersistentRoot(), "demo")
	want := map[string]string{
		"FOO_REGION":     "cn",
		"FOO_HOME":       filepath.Join(home, ".foo"),
		"FOO_CACHE":      home + "/.cache/foo",
		"FOO_SCRIPT":     "/packages/demo/bin/run.sh",
		"FOO_CONFIG_DIR": filepath.Join(state, "config"),
		"FOO_HOMEPAGE":   "$HOMEPAGE",
	}
	if len(public) != len(want) {
		t.Fatalf("public env %v", public)
	}
	for key, value := range want {
		if public[key] != value {
			t.Fatalf("%s = %q, want %q", key, public[key], value)
		}
	}
	credentials, err := pkg.CLICredentialTemplates()
	if err != nil || len(credentials) != 1 || credentials["FOO_KEY"] != "key=${API_KEY}" {
		t.Fatalf("credential templates %v %v", credentials, err)
	}
	oauth := cliEnvPackage(t, AuthMCP, map[string]any{"FOO_TOKEN": "Bearer ${OAUTH_ACCESS_TOKEN}"})
	if templates, err := oauth.CLICredentialTemplates(); err != nil || templates["FOO_TOKEN"] != "Bearer ${ACCESS_TOKEN}" {
		t.Fatalf("oauth template %v %v", templates, err)
	}
	oneid := cliEnvPackage(t, AuthOneID, map[string]any{"FOO_TOKEN": "${ONEID_TOKEN}"})
	if templates, err := oneid.CLICredentialTemplates(); err != nil || templates["FOO_TOKEN"] != "${AP_ACCESS_TOKEN}" {
		t.Fatalf("oneid template %v %v", templates, err)
	}
}
