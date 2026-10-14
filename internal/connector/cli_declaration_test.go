package connector

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const minimalTestCLI = `{"versionCheck":{"command":{"darwin":"demo --version","linux":"demo --version","win32":"demo.exe --version"},"minVersion":"1.0.0"}}`

func cliDeclarationFixture(t *testing.T) Package {
	t.Helper()
	var cli map[string]any
	if err := DecodeJSON([]byte(minimalTestCLI), &cli); err != nil {
		t.Fatal(err)
	}
	return Package{Manifest: Manifest{AuthMode: AuthToken, TokenSchema: json.RawMessage(`{"fields":[{"key":"JIRA_USERNAME","required":true},{"key":"JIRA_PASSWORD","required":true}]}`)}, CLI: cli}
}

func TestCLIComponentEnvironmentAndAliases(t *testing.T) {
	pkg := cliDeclarationFixture(t)
	pkg.CLI["env"] = map[string]any{"JIRA_USERNAME": "${JIRA_USERNAME}", "JIRA_PASSWORD": "${JIRA_PASSWORD}"}
	pkg.CLI["staticEnv"] = map[string]any{"JIRA_URL": "https://jira.example.test"}
	declaration, err := pkg.CLIExecutionEnvironment()
	if err != nil || declaration.Env["JIRA_PASSWORD"] != "${JIRA_PASSWORD}" || declaration.StaticEnv["JIRA_URL"] != "https://jira.example.test" {
		t.Fatal("component-local environment was not preserved", err)
	}
	declaration.Env["JIRA_PASSWORD"] = "changed"
	declaration.StaticEnv["JIRA_URL"] = "changed"
	if pkg.CLI["env"].(map[string]any)["JIRA_PASSWORD"] != "${JIRA_PASSWORD}" || pkg.CLI["staticEnv"].(map[string]any)["JIRA_URL"] != "https://jira.example.test" {
		t.Fatal("declaration mutated the source package")
	}
	for _, test := range []struct {
		mode             AuthMode
		public, internal string
	}{
		{AuthOneID, "ONEID_TOKEN", "AP_ACCESS_TOKEN"},
		{AuthOAuth, "OAUTH_ACCESS_TOKEN", "ACCESS_TOKEN"},
	} {
		pkg.AuthMode = test.mode
		pkg.CLI["env"] = map[string]string{"SERVICE_TOKEN": "Bearer ${" + test.public + "}"}
		declaration, err := pkg.CLIExecutionEnvironment()
		if err != nil || declaration.Env["SERVICE_TOKEN"] != "Bearer ${"+test.internal+"}" {
			t.Fatal("public authentication alias was not normalized", test.mode, err)
		}
		if pkg.CLI["env"].(map[string]string)["SERVICE_TOKEN"] != "Bearer ${"+test.public+"}" {
			t.Fatal("normalization rewrote the source declaration")
		}
		pkg.CLI["env"] = map[string]string{"SERVICE_TOKEN": "${" + test.internal + "}"}
		if _, err := pkg.CLIExecutionEnvironment(); !errors.Is(err, ErrInvalidPackage) {
			t.Fatal("component-local input accepted an internal-only alias", test.mode, err)
		}
	}
}

func TestCLIEnvironmentRejectsAmbiguousOrInvalidDeclarations(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Package)
	}{
		{"env is not object", func(p *Package) { p.CLI["env"] = []any{} }},
		{"env null", func(p *Package) { p.CLI["env"] = nil }},
		{"env non-string", func(p *Package) { p.CLI["env"] = map[string]any{"KEY": true} }},
		{"env empty", func(p *Package) { p.CLI["env"] = map[string]any{"KEY": ""} }},
		{"env whitespace", func(p *Package) { p.CLI["env"] = map[string]any{"KEY": " "} }},
		{"env lowercase", func(p *Package) { p.CLI["env"] = map[string]any{"key": "${JIRA_PASSWORD}"} }},
		{"env starts with digit", func(p *Package) { p.CLI["env"] = map[string]any{"1KEY": "${JIRA_PASSWORD}"} }},
		{"env literal", func(p *Package) { p.CLI["env"] = map[string]any{"KEY": "literal"} }},
		{"env unknown template", func(p *Package) { p.CLI["env"] = map[string]any{"KEY": "${UNKNOWN}"} }},
		{"env malformed template", func(p *Package) { p.CLI["env"] = map[string]any{"KEY": "${JIRA_PASSWORD}${bad}"} }},
		{"env linebreak", func(p *Package) { p.CLI["env"] = map[string]any{"KEY": "${JIRA_PASSWORD}\n"} }},
		{"static is not object", func(p *Package) { p.CLI["staticEnv"] = "value" }},
		{"static non-string", func(p *Package) { p.CLI["staticEnv"] = map[string]any{"KEY": 3} }},
		{"static empty", func(p *Package) { p.CLI["staticEnv"] = map[string]any{"KEY": ""} }},
		{"static template", func(p *Package) { p.CLI["staticEnv"] = map[string]any{"KEY": "${JIRA_PASSWORD}"} }},
		{"static-dynamic collision", func(p *Package) {
			p.CLI["staticEnv"] = map[string]any{"KEY": "literal"}
			p.CLI["env"] = map[string]any{"KEY": "${JIRA_PASSWORD}"}
		}},
		{"component-binding duplicate", func(p *Package) {
			p.CLI["env"] = map[string]any{"KEY": "${JIRA_PASSWORD}"}
			p.AuthBindings = map[string]AuthBinding{"cli": {Env: map[string]string{"KEY": "${JIRA_PASSWORD}"}}}
		}},
		{"static-binding collision", func(p *Package) {
			p.CLI["staticEnv"] = map[string]any{"KEY": "literal"}
			p.AuthBindings = map[string]AuthBinding{"cli": {Env: map[string]string{"KEY": "${JIRA_PASSWORD}"}}}
		}},
		{"delegated dynamic", func(p *Package) { p.AuthMode = AuthDelegated; p.CLI["env"] = map[string]any{"KEY": "${JIRA_PASSWORD}"} }},
		{"mcp dynamic", func(p *Package) { p.AuthMode = AuthMCP; p.CLI["env"] = map[string]any{"KEY": "${OAUTH_ACCESS_TOKEN}"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			pkg := cliDeclarationFixture(t)
			test.edit(&pkg)
			if _, err := pkg.CLIExecutionEnvironment(); !errors.Is(err, ErrInvalidPackage) {
				t.Fatal("invalid environment was accepted", err)
			}
		})
	}
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "USERPROFILE", "APPDATA", "XDG_CONFIG_HOME", "CONNECTOR_BIN_DIR", "CONNECTOR_ARCH", "AP_ACCESS_TOKEN", "AP_CHAT_DIR", "OTHER_CONFIG_DIR", "NPM_CONFIG_PREFIX", "BASH_ENV", "ENV", "INPUTRC", "NODE_OPTIONS", "PYTHONPATH", "LD_PRELOAD", "DYLD_LIBRARY_PATH", "GIT_CONFIG_COUNT"} {
		for _, field := range []string{"env", "staticEnv"} {
			t.Run(field+"/"+name, func(t *testing.T) {
				pkg := cliDeclarationFixture(t)
				value := "fixed"
				if field == "env" {
					value = "${JIRA_PASSWORD}"
				}
				pkg.CLI[field] = map[string]string{name: value}
				if _, err := pkg.CLIExecutionEnvironment(); !errors.Is(err, ErrInvalidPackage) {
					t.Fatal("platform-owned environment was accepted", err)
				}
			})
		}
	}
}

func TestCLIEnvironmentCompatibleBindingsDoNotOverrideComponentInput(t *testing.T) {
	pkg := cliDeclarationFixture(t)
	pkg.CLI["env"] = map[string]string{"_USER": "${JIRA_USERNAME}"}
	pkg.AuthBindings = map[string]AuthBinding{"cli": {Env: map[string]string{"PASSWORD": "${JIRA_PASSWORD}"}}}
	declaration, err := pkg.CLIExecutionEnvironment()
	if err != nil || !reflect.DeepEqual(declaration.Env, map[string]string{"_USER": "${JIRA_USERNAME}", "PASSWORD": "${JIRA_PASSWORD}"}) {
		t.Fatal("non-overlapping compatibility input was not combined", err)
	}
	for _, mode := range []AuthMode{AuthOneID, AuthOAuth, AuthMCP} {
		pkg.AuthMode = mode
		delete(pkg.CLI, "env")
		variable := "ACCESS_TOKEN"
		if mode == AuthOneID {
			variable = "AP_ACCESS_TOKEN"
		}
		pkg.AuthBindings["cli"] = AuthBinding{Env: map[string]string{"TOKEN": "${" + variable + "}"}}
		declaration, err = pkg.CLIExecutionEnvironment()
		if err != nil || declaration.Env["TOKEN"] != "${"+variable+"}" {
			t.Fatal("published authentication binding stopped working", mode, err)
		}
	}
}

func TestCLIValidatesFixedStructuresAndPreservesPublishedExtensions(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Package)
	}{
		{"unknown top-level", func(p *Package) { p.CLI["envs"] = map[string]any{} }},
		{"missing version", func(p *Package) { delete(p.CLI, "versionCheck") }},
		{"unknown version field", func(p *Package) { p.CLI["versionCheck"].(map[string]any)["minimum"] = "1.0.0" }},
		{"invalid SemVer", func(p *Package) { p.CLI["versionCheck"].(map[string]any)["minVersion"] = "1.0.0-01" }},
		{"unknown OS", func(p *Package) { p.CLI["init"] = map[string]any{"macOS": "demo init"} }},
		{"empty command", func(p *Package) { p.CLI["init"] = map[string]any{"darwin": ""} }},
		{"non-string command", func(p *Package) { p.CLI["init"] = map[string]any{"darwin": false} }},
		{"empty OS map", func(p *Package) { p.CLI["status"] = map[string]any{} }},
		{"empty auth steps", func(p *Package) { p.AuthMode = AuthDelegated; p.CLI["auth"] = []any{} }},
		{"unknown runtime field", func(p *Package) {
			p.CLI["runtime"] = map[string]any{"type": "node", "version": ">=20", "arch": "arm64"}
		}},
		{"invalid runtime shape", func(p *Package) { p.CLI["runtime"] = map[string]any{"type": "node", "version": 20} }},
		{"unknown platform field", func(p *Package) { p.CLI["platform"] = map[string]any{"commands": "demo"} }},
		{"invalid presentation extension", func(p *Package) { p.CLI["authQrModal"] = "true" }},
		{"invalid status JSON shape", func(p *Package) { p.CLI["statusMatchJson"] = []any{} }},
		{"empty status regex", func(p *Package) { p.CLI["statusMatch"] = "" }},
		{"token auth command", func(p *Package) { p.CLI["auth"] = map[string]any{"darwin": "demo auth"} }},
		{"token login option", func(p *Package) { p.CLI["authWaitForExit"] = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			pkg := cliDeclarationFixture(t)
			test.edit(&pkg)
			if err := pkg.ValidateCLI(); !errors.Is(err, ErrInvalidPackage) {
				t.Fatal("invalid CLI structure was accepted", err)
			}
		})
	}
	pkg := cliDeclarationFixture(t)
	pkg.CLI["status"] = map[string]any{"darwin": "demo status"}
	pkg.CLI["statusMatch"] = `(?=authenticated)authenticated`
	pkg.CLI["statusMatchJson"] = map[string]any{}
	pkg.CLI["versionCheck"].(map[string]any)["minVersion"] = "1.0.0-rc.1+build.1"
	if err := pkg.ValidateCLI(); err != nil {
		t.Fatal("simultaneous status conditions or full SemVer rejected", err)
	}
	pkg.AuthMode = AuthDelegated
	pkg.CLI["auth"] = []any{map[string]any{"command": map[string]any{"darwin": "demo login"}, "skipIf": map[string]any{"darwin": "demo ready"}, "authWaitForExit": false, "authSuppressBrowser": true, "authUrlDomain": "login.example.test"}}
	pkg.CLI["platform"] = map[string]any{"command": "demo", "configEnv": "DEMO_CONFIG_DIR", "logoutMode": "delete-config", "npmPackage": "demo-cli", "npmVersion": "1.0.0", "entry": "cli.js", "nativeEntry": "bin/demo"}
	pkg.CLI["authQrModal"] = true
	if err := pkg.ValidateCLI(); err != nil {
		t.Fatal("published platform/presentation extensions rejected", err)
	}
	pkg.CLI["unAuth"] = map[string]any{"darwin": ""}
	if err := pkg.ValidateCLI(); err != nil {
		t.Fatal("published delete-config logout rejected", err)
	}
}

func TestCLIDeclarationErrorsAreRejectedDuringPackageLoad(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":null}`
	if err := os.WriteFile(filepath.Join(dir, "connector.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cli := range []string{`null`, `{"versionCheck":{},"unexpected":true}`, `{"versionCheck":{"command":{"darwin":"demo --version"},"minVersion":"1.0.0"},"env":{"PASSWORD":"${UNKNOWN}"}}`} {
		if err := os.WriteFile(filepath.Join(dir, "cli.json"), []byte(cli), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "demo"); !errors.Is(err, ErrInvalidPackage) {
			t.Fatal("invalid declaration was published", err)
		}
	}
}
