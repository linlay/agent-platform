package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectionOwnerIsolationAndPreferenceLifecycle(t *testing.T) {
	ctx := context.Background()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "connectors")}
	writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "API_KEY", "type": "password", "required": true}}}}, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"})
	manager := New(ctx, sources, nil).WithCredentialValidator(acceptTokenForPersistenceTest)
	alice := manager.ForOwner("user:alice")
	bob := manager.ForOwner("user:bob")
	if _, err := alice.SetEnabled(ctx, "demo", true); err == nil {
		t.Fatal("enabled unbound connector")
	}
	if _, err := alice.SetToken(ctx, "demo", map[string]string{"API_KEY": "private-alice"}); err != nil {
		t.Fatal(err)
	}
	state, err := alice.Connection(ctx, "demo")
	if err != nil || !state.Bound || state.Enabled || state.Authentication.Status != "authorized" {
		t.Fatal(state, err)
	}
	other, err := bob.Connection(ctx, "demo")
	if err != nil || other.Bound || other.Enabled || other.Authentication.Status != "unauthorized" {
		t.Fatal(other, err)
	}
	if _, err = alice.SetEnabled(ctx, "demo", true); err != nil {
		t.Fatal(err)
	}
	pkg, _ := alice.sources.Load("demo")
	path, _ := connector.CredentialsPath(pkg.CredentialRoot(), pkg.ID)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	state, err = alice.Connection(ctx, "demo")
	if err != nil || !state.Bound || !state.Enabled || state.Readiness != "authorization_required" {
		t.Fatal("credential loss reset preferences", state, err)
	}
	if _, err = alice.SetToken(ctx, "demo", map[string]string{"API_KEY": "private-again"}); err != nil {
		t.Fatal(err)
	}
	state, err = alice.SetEnabled(ctx, "demo", false)
	if err != nil || !state.Bound || state.Enabled || state.Authentication.Status != "authorized" {
		t.Fatal(state, err)
	}
	if _, err = alice.Disconnect(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	state, err = alice.Connection(ctx, "demo")
	if err != nil || state.Bound || state.Enabled || state.Authentication.Status != "unauthorized" {
		t.Fatal(state, err)
	}
	if _, err = os.Stat(pkg.Dir); err != nil {
		t.Fatal("disconnect removed shared package", err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("credentials retained")
	}
	if !strings.Contains(path, "users") || strings.Contains(path, "alice") {
		t.Fatal("owner path not opaque")
	}
}
