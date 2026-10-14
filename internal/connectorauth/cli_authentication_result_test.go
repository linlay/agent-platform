package connectorauth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/connector"
)

func TestCLIStatusAuthenticationErrorRequiresExplicitMachineSignal(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         error
	}{
		{"denied", `{"schema_version":1,"error":{"code":"AUTHENTICATION_DENIED","http_status":403,"message":"fixture-private-diagnostic"}}`, ErrCredentialAuthenticationDenied},
		{"auth_failed", `{"error":{"code":"AUTH_FAILED"}}`, ErrTokenRejected},
		{"auth_required", `{"error":{"code":"AUTH_REQUIRED"}}`, ErrTokenRejected},
		{"unauthorized", `{"error":{"code":"UNAUTHORIZED"}}`, ErrTokenRejected},
		{"invalid_token", `{"error":{"code":"INVALID_TOKEN"}}`, ErrTokenRejected},
		{"missing_credentials", `{"error":{"code":"CREDENTIALS_MISSING"}}`, ErrTokenRejected},
		{"http_401", `{"error":{"code":"OTHER_ERROR","http_status":401}}`, ErrTokenRejected},
		{"numeric_code_http_401", `{"error":{"code":893201,"http_status":401}}`, ErrTokenRejected},
		{"generic_403", `{"error":{"code":"FORBIDDEN","http_status":403}}`, nil},
		{"network", `{"error":{"code":"NETWORK_ERROR","message":"AUTH_FAILED"}}`, nil},
		{"success", `{"authenticated":true}`, nil},
		{"root_code", `{"code":"AUTH_FAILED"}`, nil},
		{"business_data", `{"data":{"error":{"code":"AUTH_FAILED"}}}`, nil},
		{"array", `[{"error":{"code":"AUTH_FAILED"}}]`, nil},
		{"string_status", `{"error":{"http_status":"401"}}`, nil},
		{"plain_text", `AUTHENTICATION_DENIED HTTP 401`, nil},
		{"diagnostic_prefix", "diagnostic\n{\"error\":{\"code\":\"AUTH_FAILED\"}}", nil},
		{"two_objects", `{"error":{"code":"AUTH_FAILED"}} {"authenticated":true}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := cliStatusAuthenticationError(tc.output)
			if !errors.Is(got, tc.want) || (tc.want == nil && got != nil) {
				t.Fatalf("classification: got %v, want %v", got, tc.want)
			}
			if got != nil && strings.Contains(got.Error(), "fixture-private-diagnostic") {
				t.Fatal("machine diagnostics were exposed")
			}
		})
	}
	if !errors.Is(ErrCredentialAuthenticationDenied, ErrTokenRejected) {
		t.Fatal("authentication denial must preserve the rejected-credential category")
	}
}

// This helper really starts the declared CLI in its prepared, private process
// environment. All responses and credential values belong to this local fixture.
func authenticationResultCLIFixture(t *testing.T, token, polling bool) (*Manager, connector.Package, string) {
	t.Helper()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	modePath := filepath.Join(t.TempDir(), "status-mode")
	if err := os.WriteFile(modePath, []byte("auto"), 0600); err != nil {
		t.Fatal(err)
	}
	cli := simpleCLI()
	cli["status"] = osCommands("demo status")
	cli["statusMatchJson"] = map[string]any{"authenticated": true}
	cli["staticEnv"] = map[string]any{"TEST_STATUS_MODE_PATH": modePath}
	if token {
		cli["env"] = map[string]any{"API_KEY": "${API_KEY}"}
	} else {
		cli["auth"] = osCommands("demo login")
		cli["unAuth"] = osCommands("demo logout")
		cli["authWaitForExit"] = !polling
	}
	// The token schema must exist before validating a credential env reference.
	if token {
		dir := filepath.Join(sources.ExternalRoot, "demo")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		manifest := map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "cli", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "API_KEY", "label": "API Key", "type": "password", "required": true}}}}
		for name, value := range map[string]any{"connector.json": manifest, "cli.json": cli} {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		writeCLIPackage(t, sources.ExternalRoot, "demo", cli)
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "demo.js")
	body := `
const fs=require('fs'),modePath=process.env.TEST_STATUS_MODE_PATH,command=process.argv[2];
if(command==='--version'){console.log('1.2.0');process.exit(0)}
if(command==='login'){
  fs.writeFileSync(modePath+'.login-called','yes');
  setTimeout(()=>{fs.writeFileSync(modePath,'good-secret');process.exit(0)},700);
}else if(command==='logout'){fs.writeFileSync(modePath,'AUTH_REQUIRED');process.exit(0)}
else{
  let mode=fs.readFileSync(modePath,'utf8');
  if(mode==='auto') mode=process.env.API_KEY||'AUTH_REQUIRED';
  if(mode==='good-secret'){console.log(JSON.stringify({authenticated:true}));process.exit(0)}
  if(/^exit[0-9]+$/.test(mode)) process.exit(Number(mode.slice(4)));
  if(mode==='signal'){process.kill(process.pid,'SIGTERM');return;}
  if(mode==='network-exit1'){console.error(JSON.stringify({error:{code:'NETWORK_ERROR'}}));process.exit(1)}
  if(mode==='plain'){console.error('AUTHENTICATION_DENIED: fixture-private-diagnostic');process.exit(3)}
  const code=mode==='wrong-secret'?'AUTH_FAILED':mode==='stdout-denied'?'AUTHENTICATION_DENIED':mode==='http401'?'OTHER_ERROR':mode;
  const http_status=mode==='NETWORK_ERROR'?503:mode==='wrong-secret'||mode==='http401'?401:403;
  const error={schema_version:1,error:{code,http_status,message:'fixture-private-diagnostic '+(process.env.API_KEY||'')}};
  (mode==='stdout-denied'?console.log:console.error)(JSON.stringify(error));process.exit(3);
}`
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	testNodeCLI(t, script)
	m := New(t.Context(), sources, nil)
	if _, err := m.Prepare(t.Context(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	return m, pkg, modePath
}

func TestTokenCLIStructuredValidationPreservesActiveBinding(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, tc := range []struct {
			candidate string
			want      error
		}{
			{"wrong-secret", ErrTokenRejected},
			{"AUTHENTICATION_DENIED", ErrCredentialAuthenticationDenied},
			{"stdout-denied", ErrCredentialAuthenticationDenied},
			{"http401", ErrTokenRejected},
			{"NETWORK_ERROR", nil},
			{"FORBIDDEN", nil},
			{"plain", nil},
		} {
			name := tc.candidate + "/initial"
			if existing {
				name = tc.candidate + "/update"
			}
			t.Run(name, func(t *testing.T) {
				m, pkg, _ := authenticationResultCLIFixture(t, true, false)
				if existing {
					got, err := m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": "good-secret"})
					if err != nil || got.Status != "authorized" {
						t.Fatalf("valid credential commit: %v %v", got, err)
					}
				}
				got, err := m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": tc.candidate})
				if tc.want != nil {
					if !errors.Is(err, tc.want) || got.PendingVerification {
						t.Fatalf("explicit rejection became pending: %v %v", got, err)
					}
					if tc.want == ErrTokenRejected && errors.Is(err, ErrCredentialAuthenticationDenied) {
						t.Fatal("ordinary authentication failure became service denial")
					}
				} else if err != nil || got.Status != "pending_verification" || !got.PendingVerification {
					t.Fatalf("unavailable check rejected credentials: %v %v", got, err)
				}
				if err != nil && strings.Contains(err.Error(), "fixture-private-diagnostic") || strings.Contains(got.Message, "fixture-private-diagnostic") {
					t.Fatal("private diagnostics were exposed")
				}
				state, stateErr := pkg.ReadConnection()
				if stateErr != nil || state.Configured != existing {
					t.Fatalf("candidate changed active binding: %v %v", state, stateErr)
				}
				values, ready, valueErr := TokenValues(pkg)
				if valueErr != nil || ready != existing || existing && values["API_KEY"] != "good-secret" {
					t.Fatalf("candidate replaced active credentials: ready=%v error=%v", ready, valueErr)
				}
				_, pending, pendingErr := pendingTokenValues(pkg)
				if pendingErr != nil || pending != (tc.want == nil) {
					t.Fatalf("wrong pending candidate state: %v %v", pending, pendingErr)
				}
				connection, connectionErr := m.Connection(t.Context(), pkg.ID)
				if connectionErr != nil {
					t.Fatal(connectionErr)
				}
				if !existing && tc.want == nil && connection.Readiness != "pending_verification" {
					t.Fatalf("pending first submission hidden: %v", connection.Readiness)
				}
				if existing && (connection.Readiness != "ready" || connection.Authentication.Status != "authorized") {
					t.Fatalf("candidate disabled old account: %v", connection)
				}
			})
		}
	}
}

func TestDelegatedCLICheckDistinguishesAuthenticationFromExecutionFailure(t *testing.T) {
	for _, tc := range []struct{ mode, status string }{
		{"AUTHENTICATION_DENIED", "unauthorized"}, {"AUTH_REQUIRED", "unauthorized"},
		{"NETWORK_ERROR", "failed"}, {"FORBIDDEN", "failed"}, {"plain", "failed"},
		{"good-secret", "authorized"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			m, pkg, modePath := authenticationResultCLIFixture(t, false, false)
			if err := os.WriteFile(modePath, []byte(tc.mode), 0600); err != nil {
				t.Fatal(err)
			}
			dir, err := pkg.ConnectorStateDir()
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "home", "login-material")
			if err := os.WriteFile(marker, []byte("fixture-local-login"), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := m.Check(t.Context(), pkg.ID, "")
			if err != nil || got.Status != tc.status || strings.Contains(got.Message, "fixture-private-diagnostic") {
				t.Fatalf("status classification: %v %v", got, err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("check removed stored login materials", err)
			}
		})
	}
}

func TestPendingCLICandidateSurvivesExplicitDenialUntilRecovery(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "initial"
		if existing {
			name = "update"
		}
		t.Run(name, func(t *testing.T) {
			m, pkg, modePath := authenticationResultCLIFixture(t, true, false)
			if existing {
				if _, err := m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": "good-secret"}); err != nil {
					t.Fatal(err)
				}
			}
			candidate := map[string]string{"API_KEY": "NETWORK_ERROR"}
			if got, err := m.SetToken(t.Context(), pkg.ID, candidate); err != nil || got.Status != "pending_verification" {
				t.Fatalf("candidate was not staged: %v %v", got, err)
			}
			if err := os.WriteFile(modePath, []byte("AUTHENTICATION_DENIED"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Check(t.Context(), pkg.ID, ""); !errors.Is(err, ErrCredentialAuthenticationDenied) {
				t.Fatalf("pending candidate denial was hidden: %v", err)
			}
			values, pending, err := pendingTokenValues(pkg)
			if err != nil || !pending || values["API_KEY"] != candidate["API_KEY"] {
				t.Fatal("service denial discarded the candidate needed for recovery", err)
			}
			state, err := pkg.ReadConnection()
			if err != nil || state.Configured != existing {
				t.Fatal("denied candidate changed the existing binding", err)
			}
			// Simulate service-side recovery without changing the saved candidate.
			if err := os.WriteFile(modePath, []byte("good-secret"), 0600); err != nil {
				t.Fatal(err)
			}
			if got, err := m.Check(t.Context(), pkg.ID, ""); err != nil || got.Status != "authorized" {
				t.Fatalf("recovered candidate did not commit: %v %v", got, err)
			}
			values, ready, err := TokenValues(pkg)
			if err != nil || !ready || values["API_KEY"] != candidate["API_KEY"] {
				t.Fatal("verified candidate was not committed", err)
			}
			if _, pending, err := pendingTokenValues(pkg); err != nil || pending {
				t.Fatal("verified candidate remained pending", err)
			}
		})
	}
}

func TestCLILoginContinuesAfterExplicitUnauthenticatedStatus(t *testing.T) {
	for _, polling := range []bool{false, true} {
		t.Run(map[bool]string{false: "wait_for_exit", true: "polling"}[polling], func(t *testing.T) {
			m, pkg, modePath := authenticationResultCLIFixture(t, false, polling)
			if err := os.WriteFile(modePath, []byte("AUTHENTICATION_DENIED"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			if err := m.loginCLI(ctx, pkg, &login{}); err != nil {
				t.Fatal("explicit authentication failure prevented login recovery", err)
			}
			if ok, err := m.cliStatus(ctx, pkg); err != nil || !ok {
				t.Fatalf("recovery did not verify final status: %v %v", ok, err)
			}
		})
	}
	// A network/startup failure remains a failed preflight, not permission to
	// silently start another login or modify the existing account.
	m, pkg, modePath := authenticationResultCLIFixture(t, false, false)
	if err := os.WriteFile(modePath, []byte("NETWORK_ERROR"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.loginCLI(t.Context(), pkg, &login{}); err == nil || errors.Is(err, ErrTokenRejected) {
		t.Fatalf("execution failure was treated as missing authentication: %v", err)
	}
	if _, err := os.Stat(modePath + ".login-called"); !os.IsNotExist(err) {
		t.Fatal("failed status check started login", err)
	}
}

func TestCLILoginRecoversFromUnconfirmedExitOne(t *testing.T) {
	for _, polling := range []bool{false, true} {
		t.Run(map[bool]string{false: "wait_for_exit", true: "polling"}[polling], func(t *testing.T) {
			m, pkg, modePath := authenticationResultCLIFixture(t, false, polling)
			if err := os.WriteFile(modePath, []byte("exit1"), 0600); err != nil {
				t.Fatal(err)
			}
			if checked, err := m.Check(t.Context(), pkg.ID, ""); err != nil || checked.Status != "failed" {
				t.Fatalf("inconclusive status was classified as invalid credentials: %+v %v", checked, err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			if err := m.loginCLI(ctx, pkg, &login{}); err != nil {
				t.Fatal("exit 1 blocked explicit login", err)
			}
			if ok, err := m.cliStatus(ctx, pkg); err != nil || !ok {
				t.Fatalf("login did not finish: %v %v", ok, err)
			}
		})
	}
}

func TestCLIStatusExecutionFailuresDoNotStartOrContinueLogin(t *testing.T) {
	for _, mode := range []string{"exit2", "exit126", "exit127", "signal", "network-exit1"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "signal" && runtime.GOOS == "windows" {
				t.Skip("POSIX signal exit semantics")
			}
			m, pkg, modePath := authenticationResultCLIFixture(t, false, true)
			if err := os.WriteFile(modePath, []byte(mode), 0600); err != nil {
				t.Fatal(err)
			}
			if err := m.loginCLI(t.Context(), pkg, &login{}); err == nil || cliStatusAllowsLogin(err) {
				t.Fatalf("execution failure allowed login: %v", err)
			}
			if _, err := os.Stat(modePath + ".login-called"); !os.IsNotExist(err) {
				t.Fatal("failed preflight ran auth")
			}
			if err := m.runCLIAuthStep(t.Context(), pkg, &login{}, cliAuthStep{command: "demo login"}); err == nil {
				t.Fatal("execution failure did not stop the pending login")
			}
			if contents, err := os.ReadFile(modePath); err != nil || string(contents) != mode {
				t.Fatal("failed login changed the existing status")
			}
		})
	}
}

func TestCLIStatusUnavailableCommandAndDeadlineDoNotStartLogin(t *testing.T) {
	for _, failure := range []string{"missing_command", "deadline"} {
		t.Run(failure, func(t *testing.T) {
			m, pkg, modePath := authenticationResultCLIFixture(t, false, false)
			ctx := t.Context()
			if failure == "missing_command" {
				pkg.CLI["status"] = osCommands("codex-fixture-command-that-does-not-exist")
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			if err := m.loginCLI(ctx, pkg, &login{}); err == nil || cliStatusAllowsLogin(err) {
				t.Fatalf("%s allowed login: %v", failure, err)
			}
			if _, err := os.Stat(modePath + ".login-called"); !os.IsNotExist(err) {
				t.Fatal("failed preflight ran auth")
			}
		})
	}
}

func TestCLIStatusExitOnePreservesSavedToken(t *testing.T) {
	m, pkg, modePath := authenticationResultCLIFixture(t, true, false)
	if _, err := m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": "good-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modePath, []byte("exit1"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": "candidate"}); err != nil || got.Status != "pending_verification" {
		t.Fatalf("inconclusive candidate was not staged: %+v %v", got, err)
	}
	values, ready, err := TokenValues(pkg)
	if err != nil || !ready || values["API_KEY"] != "good-secret" {
		t.Fatal("inconclusive status replaced the saved credentials", err)
	}
}

func TestCLIAuthStepLimitBeforeExecutingAnyStep(t *testing.T) {
	m, pkg, modePath := authenticationResultCLIFixture(t, false, false)
	for _, count := range []int{0, 16, 17} {
		steps := make([]any, count)
		for i := range steps {
			steps[i] = map[string]any{"command": osCommands("demo login")}
		}
		pkg.CLI["auth"] = steps
		_, err := cliAuthSteps(pkg)
		if (err == nil) != (count == 16) {
			t.Fatalf("step count %d: %v", count, err)
		}
		if err := pkg.ValidateCLI(); (err == nil) != (count == 16) {
			t.Fatalf("declaration step count %d: %v", count, err)
		}
	}
	if err := m.loginCLI(t.Context(), pkg, &login{}); err == nil {
		t.Fatal("oversized login started")
	}
	if _, err := os.Stat(modePath + ".login-called"); !os.IsNotExist(err) {
		t.Fatal("oversized login executed an earlier step")
	}
}

func TestConnectionFailedAndCanceledPreparationIsUnavailable(t *testing.T) {
	for _, status := range []string{"failed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
			pkg := writeCLIPackage(t, sources.ExternalRoot, "demo", simpleCLI())
			fingerprint, err := connector.RuntimeFingerprint(pkg.Dir)
			if err != nil {
				t.Fatal(err)
			}
			m := New(t.Context(), sources, nil)
			if err := m.savePreparation(Preparation{ConnectorID: pkg.ID, Status: status, Fingerprint: fingerprint, Message: "fixture-private-diagnostic", Diagnostic: "fixture-private-diagnostic"}); err != nil {
				t.Fatal(err)
			}
			got, err := m.Connection(t.Context(), pkg.ID)
			if err != nil || got.Readiness != "unavailable" || got.Authentication.Status != "setup_required" {
				t.Fatalf("terminal preparation kept spinner: %v %v", got, err)
			}
			if got.Preparation == nil || got.Preparation.Status != status || got.Preparation.Diagnostic != "" || strings.Contains(got.Preparation.Message+got.Authentication.Message, "fixture-private-diagnostic") {
				t.Fatal("preparation diagnostics were exposed")
			}
		})
	}
}
