package connectorauth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"agent-platform/internal/connector"
)

func TestCLIVersionPatternJSONEscapingAndPreparationDiagnostic(t *testing.T) {
	script := filepath.Join(t.TempDir(), "cli.js")
	if err := os.WriteFile(script, []byte(`console.log('wecom-cli 1.3.0')`), 0600); err != nil {
		t.Fatal(err)
	}
	testNodeCLI(t, script)
	for _, test := range []struct {
		name, pattern, wantDiagnostic string
	}{
		{name: "correct single escape", pattern: `^wecom-cli\s+(\d+\.\d+\.\d+)`},
		{name: "incorrect double escape", pattern: `^wecom-cli\\s+(\\d+\\.\\d+\\.\\d+)`, wantDiagnostic: "version output does not match versionPattern"},
		{name: "missing pattern", wantDiagnostic: "version output must be SemVer or declare versionPattern"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			cli := simpleCLI()
			check := cli["versionCheck"].(map[string]any)
			check["minVersion"] = "1.3.0"
			if test.pattern != "" {
				check["versionPattern"] = test.pattern
			}
			// A real JSON package load must preserve its decoded regex exactly,
			// rather than normalizing an author's incorrectly double-escaped text.
			pkg := writeCLIPackage(t, root, "demo", cli)
			if got, _ := pkg.CLI["versionCheck"].(map[string]any)["versionPattern"].(string); got != test.pattern {
				t.Fatalf("package regex changed during load: got %q, want %q", got, test.pattern)
			}
			m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
			if test.wantDiagnostic != "" {
				var execution *CLIExecutionError
				if checkErr := m.checkCLIVersion(context.Background(), pkg); !errors.As(checkErr, &execution) || execution.ExitCode != 0 || execution.Stage != "versionCheck" {
					t.Fatalf("version mismatch was not classified as output validation: %v", checkErr)
				}
			}
			state, err := m.Prepare(context.Background(), "demo")
			if test.wantDiagnostic == "" {
				if err != nil || state.Status != "ready" {
					t.Fatalf("correct escaped package failed: %+v %v", state, err)
				}
				return
			}
			if err == nil || state.Status != "failed" || state.Stage != "versionCheck" || state.ExitCode == nil || *state.ExitCode != 0 {
				t.Fatalf("output validation did not retain process success: %+v %v", state, err)
			}
			if !strings.Contains(state.Message, test.wantDiagnostic) || !strings.Contains(state.Diagnostic, test.wantDiagnostic) || strings.Contains(state.Message, "failed (exit 0)") {
				t.Fatalf("misleading version check diagnostic: %+v", state)
			}
		})
	}
}

func TestCLIPreparationDiagnosticRedactionAndBound(t *testing.T) {
	pkg := connector.Package{CLI: map[string]any{"staticEnv": map[string]any{"CUSTOM_VALUE": "private-static"}}}
	output := "npm EACCES: permission denied\nprivate-static raw-env-secret\n" +
		"Authorization: Bearer bearer-secret\nAuthorization: Basic dXNlcjpwYXNz\npassword=quoted-secret\n" +
		"//registry.example.test/:_authToken=fake-npm-token\n_auth=fake-basic-token\nNPM_CONFIG__AUTH=fake-npm-auth\n_password='encoded password'\n" +
		"https://proxy-user:proxy-secret@proxy.example.test/\n" + strings.Repeat("故障", 3000)
	diagnostic := cliPreparationDiagnostic("CLI initialization failed", output, pkg, []string{"CUSTOM_TOKEN=raw-env-secret"})
	for _, secret := range []string{"private-static", "raw-env-secret", "bearer-secret", "dXNlcjpwYXNz", "quoted-secret", "proxy-user", "proxy-secret", "fake-npm-token", "fake-basic-token", "fake-npm-auth", "encoded password"} {
		if strings.Contains(diagnostic, secret) {
			t.Errorf("diagnostic leaked %s", secret)
		}
	}
	if !strings.Contains(diagnostic, "EACCES: permission denied") || !strings.Contains(diagnostic, "proxy.example.test") || !strings.HasSuffix(diagnostic, "[truncated]") || utf8.RuneCountInString(diagnostic) > 2100 {
		t.Fatalf("missing useful bounded diagnostic: %q", diagnostic)
	}
}

func TestCLIOutputDrainFailureDoesNotExposeDiagnostic(t *testing.T) {
	failure := &CLIExecutionError{Stage: "init", ExitCode: 0, Diagnostic: "private process output", Cause: exec.ErrWaitDelay}
	if strings.Contains(failure.Error(), failure.Diagnostic) || !errors.Is(failure, exec.ErrWaitDelay) {
		t.Fatal("successful process exit exposed a failed execution's private output")
	}
}

func TestCLIVersionFailureRetainsPrivateDiagnostic(t *testing.T) {
	script := filepath.Join(t.TempDir(), "version.js")
	if err := os.WriteFile(script, []byte("console.log('loader failed'); console.error('EACCES permission denied token=private-value'); process.exit(7)"), 0600); err != nil {
		t.Fatal(err)
	}
	testNodeCLI(t, script)
	root := t.TempDir()
	pkg := writeCLIPackage(t, root, "demo", simpleCLI())
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil)
	err := m.checkCLIVersion(context.Background(), pkg)
	var failure *CLIExecutionError
	if !errors.As(err, &failure) || failure.Stage != "versionCheck" || failure.ExitCode != 7 {
		t.Fatalf("wrong preparation failure: %v", err)
	}
	if !strings.Contains(failure.Diagnostic, "EACCES") || !strings.Contains(failure.Diagnostic, "loader failed") || strings.Contains(failure.Diagnostic, "private-value") || strings.Contains(err.Error(), "EACCES") {
		t.Fatalf("diagnostic missing or exposed in ordinary error: %+v", failure)
	}
}
