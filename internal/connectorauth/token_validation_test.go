package connectorauth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/connector"
)

func tokenCLIFixture(t *testing.T, withStatus bool) (*Manager, connector.Package) {
	t.Helper()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "state")}
	cli := simpleCLI()
	cli["env"] = map[string]any{"API_KEY": "${API_KEY}"}
	if withStatus {
		cli["status"] = osCommands("demo status")
		cli["statusMatchJson"] = map[string]any{"authenticated": true}
	}
	pkg := writeCLIPackage(t, sources.ExternalRoot, "demo", cli)
	manifest := pkg.Manifest
	manifest.AuthMode = connector.AuthToken
	manifest.AuthBindings = map[string]connector.AuthBinding{"cli": {Env: map[string]string{"API_KEY": "${API_KEY}"}}}
	manifest.TokenSchema = json.RawMessage(`{"fields":[{"key":"API_KEY","label":"API Key","type":"password","required":true}]}`)
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pkg.Dir, "connector.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "demo.js")
	body := `const fs=require('fs'),path=require('path');if(process.argv[2]==='--version'){console.log('1.2.0');process.exit(0)}fs.mkdirSync(process.env.HOME,{recursive:true});fs.writeFileSync(path.join(process.env.HOME,'probe'),process.env.API_KEY||'missing');console.log(JSON.stringify({authenticated:process.env.API_KEY==='good-secret'}));`
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	testNodeCLI(t, script)
	m := New(t.Context(), sources, nil)
	if _, err := m.Prepare(t.Context(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	return m, pkg
}

func TestTokenCLIValidatesCandidateEnvironmentBeforeCommit(t *testing.T) {
	m, pkg := tokenCLIFixture(t, true)
	session, err := m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": "good-secret"})
	if err != nil || session.Status != "authorized" {
		t.Fatal(session, err)
	}
	yes := true
	if _, err = pkg.UpdateConnection(nil, &yes); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": "wrong-secret"}); !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("wrong token accepted: %v", err)
	}
	values, ready, err := TokenValues(pkg)
	if err != nil || !ready || values["API_KEY"] != "good-secret" {
		t.Fatal("failed validation replaced old token", values, err)
	}
	state, err := pkg.ReadConnection()
	if err != nil || !state.Bound || !state.Enabled {
		t.Fatal("failed validation changed preferences", state, err)
	}
	private, _ := pkg.ConnectorStateDir()
	if _, err = os.Stat(filepath.Join(private, "home", "probe")); !os.IsNotExist(err) {
		t.Fatal("candidate status used active connector HOME", err)
	}
	status, err := m.Status(t.Context(), pkg.ID)
	if err != nil || status.Status != "authorized" {
		t.Fatal(status, err)
	}
}

func TestTokenCLIWithoutStatusIsConfiguredAndMayBeEnabled(t *testing.T) {
	m, pkg := tokenCLIFixture(t, false)
	session, err := m.SetToken(t.Context(), pkg.ID, map[string]string{"API_KEY": "unverified-secret"})
	if err != nil || session.Status != "configured" {
		t.Fatal(session, err)
	}
	connection, err := m.SetEnabled(t.Context(), pkg.ID, true)
	if err != nil || !connection.Enabled || connection.Readiness != "ready" || connection.Authentication.Status != "configured" {
		t.Fatal(connection, err)
	}
}

func TestDisconnectCancelsTokenValidationWithoutLateCommit(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "state")}
	writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "API_KEY", "required": true}}}}, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"})
	entered := make(chan struct{})
	var candidateRoot string
	m := New(t.Context(), sources, nil).WithCredentialValidator(func(ctx context.Context, pkg connector.Package, values map[string]string) error {
		candidateRoot = pkg.CredentialRoot()
		if pkg.CredentialRoot() == sources.PersistentRoot() {
			t.Error("validator received live state root")
		}
		staged, ready, err := TokenValues(pkg)
		if err != nil || !ready || staged["API_KEY"] != values["API_KEY"] {
			t.Error("candidate was not injected")
		}
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	done := make(chan error, 1)
	go func() {
		_, err := m.SetToken(t.Context(), "demo", map[string]string{"API_KEY": "never-commit"})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("validator did not start")
	}
	if _, err := m.Disconnect(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || strings.Contains(err.Error(), "never-commit") {
		t.Fatal("canceled validation succeeded or leaked", err)
	}
	if _, err := os.Stat(candidateRoot); !os.IsNotExist(err) {
		t.Fatal("candidate root retained", err)
	}
	pkg, _ := sources.Load("demo")
	if _, ready, err := TokenValues(pkg); err != nil || ready {
		t.Fatal("late credentials committed", err)
	}
	state, err := pkg.ReadConnection()
	if err != nil || state.Bound || state.Enabled {
		t.Fatal("late binding", state, err)
	}
}
