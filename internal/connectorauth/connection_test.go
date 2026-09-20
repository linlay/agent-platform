package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"testing"
	"time"
)

func connectionFixture(t *testing.T) (*Manager, connector.Package) {
	t.Helper()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "none"}, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"})
	m := New(context.Background(), sources, nil)
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	return m, pkg
}

func TestConnectionPreferenceLifecycleAndFrozenRun(t *testing.T) {
	ctx := context.Background()
	m, pkg := connectionFixture(t)
	if _, err := m.SetEnabled(ctx, "demo", true); err == nil {
		t.Fatal("enabled an unbound connector")
	}
	if _, err := m.Connect("demo"); err != nil {
		t.Fatal(err)
	}
	state, err := m.Connection(ctx, "demo")
	if err != nil || !state.Bound || state.Enabled {
		t.Fatal(state, err)
	}
	reopened := New(ctx, m.sources, nil)
	state, err = reopened.Connection(ctx, "demo")
	if err != nil || !state.Bound || state.Enabled {
		t.Fatal("preference not durable", state, err)
	}
	locator := []connector.CredentialEnvironment{{Root: pkg.PersistentRoot(), ID: pkg.ID, Mode: pkg.AuthMode}}
	if _, err := ResolveEnvironments(ctx, locator, ""); err == nil {
		t.Fatal("disabled frozen Run permitted")
	}
	if _, err := m.SetEnabled(ctx, "demo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveEnvironments(ctx, locator, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetEnabled(ctx, "demo", false); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveEnvironments(ctx, locator, ""); err == nil {
		t.Fatal("old Run bypassed disable")
	}
	generation := m.epoch("demo")
	result, err := m.Disconnect(ctx, "demo")
	if err != nil || result.Bound || result.Enabled {
		t.Fatal(result, err)
	}
	if err := m.markBoundAt(pkg, generation); err == nil {
		t.Fatal("retired authorization restored binding")
	}
	if _, err := ResolveEnvironments(ctx, locator, ""); err == nil {
		t.Fatal("old Run bypassed disconnect")
	}
}

func TestDisconnectCancelsAndWaitsForBusiness(t *testing.T) {
	ctx := context.Background()
	m, _ := connectionFixture(t)
	if _, err := m.Connect("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetEnabled(ctx, "demo", true); err != nil {
		t.Fatal(err)
	}
	child, release, err := m.BeginBusiness(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Disconnect(ctx, "demo"); done <- err }()
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("operation not canceled")
	}
	select {
	case err := <-done:
		t.Fatal("cleanup raced operation", err)
	default:
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not finish")
	}
	if _, _, err := m.BeginBusiness(ctx, "demo"); err == nil {
		t.Fatal("operation admitted after disconnect")
	}
}
