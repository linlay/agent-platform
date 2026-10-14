package connectorauth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/connector"
)

func TestCLIPhaseEnvironmentOnlyReadsCredentialsForStatusAndBusiness(t *testing.T) {
	pkg := connector.Package{Manifest: connector.Manifest{ID: "jira", AuthMode: connector.AuthToken, TokenSchema: json.RawMessage(`{"fields":[{"key":"JIRA_USERNAME","required":true},{"key":"JIRA_PASSWORD","required":true}]}`)}, StateRoot: t.TempDir(), CLI: map[string]any{
		"env":       map[string]any{"JIRA_USERNAME": "${JIRA_USERNAME}", "JIRA_PASSWORD": "${JIRA_PASSWORD}"},
		"staticEnv": map[string]any{"JIRA_URL": "https://jira.example.test"},
	}}
	binding, err := CLIEnvironment(pkg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, phase := range []CLIPhase{CLIPhaseInit, CLIPhaseVersionCheck, CLIPhaseAuth, CLIPhaseSkipIf, CLIPhaseUnAuth} {
		env, err := ResolveCLIEnvironment(ctx, binding, "", phase)
		if err != nil || !reflect.DeepEqual(env, map[string]string{"JIRA_URL": "https://jira.example.test"}) {
			t.Fatal("preparation/login environment tried to consume credentials", phase, err)
		}
	}
	path, err := connector.CredentialsPath(binding.Root, binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("public environment preparation created credential state", err)
	}
	if _, err = ResolveCLIEnvironment(ctx, binding, "", CLIPhaseBusiness); err == nil {
		t.Fatal("missing credentials were silently ignored")
	}
	for _, password := range []string{"test-password-one", "test-password-two"} {
		if err := savePrivateJSON(path, map[string]string{"JIRA_USERNAME": "test-user", "JIRA_PASSWORD": password}); err != nil {
			t.Fatal(err)
		}
		for _, phase := range []CLIPhase{CLIPhaseStatus, CLIPhaseBusiness} {
			env, err := ResolveCLIEnvironment(ctx, binding, "", phase)
			if err != nil || env["JIRA_PASSWORD"] != password || env["JIRA_USERNAME"] != "test-user" || env["JIRA_URL"] != "https://jira.example.test" {
				t.Fatal("credentialed call did not consume current component environment", phase, err)
			}
			env["JIRA_URL"] = "modified"
			if binding.StaticEnv["JIRA_URL"] != "https://jira.example.test" {
				t.Fatal("resolution mutated the frozen environment")
			}
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCLIEnvironment(ctx, binding, "", CLIPhaseStatus); err == nil {
		t.Fatal("logout retained stale credentials")
	}
	if _, err := ResolveCLIEnvironment(ctx, binding, "", CLIPhase("unknown")); err == nil {
		t.Fatal("unknown phase accepted")
	}
}

func TestCLIEnvironmentFreezesStaticOnlyAndPublicIdentityAlias(t *testing.T) {
	pkg := connector.Package{Manifest: connector.Manifest{ID: "demo", AuthMode: connector.AuthDelegated}, StateRoot: t.TempDir(), CLI: map[string]any{"staticEnv": map[string]string{"SERVICE_URL": "https://service.example.test"}}}
	binding, err := CLIEnvironment(pkg)
	if err != nil || len(binding.Env) != 0 || len(binding.StaticEnv) != 1 {
		t.Fatal("static-only connector was discarded", err)
	}
	env, err := ResolveEnvironment(t.Context(), binding, "")
	if err != nil || env["SERVICE_URL"] != "https://service.example.test" {
		t.Fatal("static-only business environment missing", err)
	}
	pkg.AuthMode = connector.AuthOneID
	pkg.CLI["env"] = map[string]string{"SERVICE_TOKEN": "${ONEID_TOKEN}"}
	binding, err = CLIEnvironment(pkg)
	if err != nil || binding.Env["SERVICE_TOKEN"] != "${AP_ACCESS_TOKEN}" {
		t.Fatal("public identity alias did not freeze as a locator", err)
	}
	if _, err := ResolveCLIEnvironment(t.Context(), binding, "", CLIPhaseVersionCheck); err != nil {
		t.Fatal("preparation required identity", err)
	}
	if _, err := ResolveCLIEnvironment(t.Context(), binding, "", CLIPhaseBusiness); err == nil {
		t.Fatal("identity was read from process environment")
	}
}

func TestPendingTokenCredentialsExplainVerificationWithoutEnteringEnvironment(t *testing.T) {
	binding := connector.CredentialEnvironment{Root: t.TempDir(), ID: "demo", Mode: connector.AuthToken,
		Env: map[string]string{"SERVICE_TOKEN": "${API_KEY}"}, StaticEnv: map[string]string{"SERVICE_URL": "https://service.example.test"}}
	dir, err := StateDir(binding.Root, binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(dir, "pending-credentials.json")
	if err := savePrivateJSON(pendingPath, map[string]string{"API_KEY": "candidate-private-test-value"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []CLIPhase{CLIPhaseStatus, CLIPhaseBusiness} {
		env, err := ResolveCLIEnvironment(t.Context(), binding, "", phase)
		if err == nil || env != nil {
			t.Fatalf("pending candidate was made executable: environment=%v error=%v", env != nil, err != nil)
		}
		for _, phrase := range []string{"saved", "awaiting verification", "Check", "no need to re-enter"} {
			if !strings.Contains(err.Error(), phrase) {
				t.Fatalf("pending credential guidance missing %q", phrase)
			}
		}
		if strings.Contains(err.Error(), "candidate-private-test-value") {
			t.Fatal("pending credential value was exposed")
		}
	}
	after, err := os.ReadFile(pendingPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("environment resolution modified the pending candidate", err)
	}
	activePath, err := connector.CredentialsPath(binding.Root, binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(activePath); !os.IsNotExist(err) {
		t.Fatal("environment resolution activated the pending candidate", err)
	}
	if err := savePrivateJSON(activePath, map[string]string{"API_KEY": "active-private-test-value"}); err != nil {
		t.Fatal(err)
	}
	env, err := ResolveEnvironment(t.Context(), binding, "")
	if err != nil || env["SERVICE_TOKEN"] != "active-private-test-value" || env["SERVICE_URL"] != "https://service.example.test" {
		t.Fatal("pending candidate displaced active credentials", err)
	}
}

func TestMissingTokenCredentialsKeepOriginalErrorWithoutUsablePendingState(t *testing.T) {
	for _, pending := range []string{"", "{}", "null", "not-json"} {
		binding := connector.CredentialEnvironment{Root: t.TempDir(), ID: "demo", Mode: connector.AuthToken,
			Env: map[string]string{"SERVICE_TOKEN": "${API_KEY}"}}
		if pending != "" {
			dir, err := StateDir(binding.Root, binding.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "pending-credentials.json"), []byte(pending), 0600); err != nil {
				t.Fatal(err)
			}
		}
		env, err := ResolveEnvironment(t.Context(), binding, "")
		if err == nil || env != nil || err.Error() != "connector demo requires credentials" {
			t.Fatal("missing credentials no longer reported accurately")
		}
	}
}
