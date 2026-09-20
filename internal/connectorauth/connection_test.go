package connectorauth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
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
func TestConnectionConfigurationLifecycle(t *testing.T) {
	m, pkg := connectionFixture(t)
	ctx := t.Context()
	if err := connector.RequireConfigured(pkg.PersistentRoot(), pkg.ID); err == nil {
		t.Fatal("unconfigured connector admitted")
	}
	if _, err := m.Connect(pkg.ID); err != nil {
		t.Fatal(err)
	}
	state, err := New(ctx, m.sources, nil).Connection(ctx, pkg.ID)
	if err != nil || !state.Configured || state.Readiness != "ready" {
		t.Fatal(state, err)
	}
	if err := connector.RequireConfigured(pkg.PersistentRoot(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	generation := m.epoch(pkg.ID)
	dir, _ := pkg.ConnectorStateDir()
	for _, name := range []string{"config/settings", "data/keep", "home/profile", "cache/item"} {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0700)
		os.WriteFile(p, []byte("keep"), 0600)
	}
	result, err := m.Disconnect(ctx, pkg.ID)
	if err != nil || result.Configured {
		t.Fatal(result, err)
	}
	if err := m.markConfiguredAt(pkg, generation); err == nil {
		t.Fatal("late login restored configuration")
	}
	for _, name := range []string{"config/settings", "data/keep", "home/profile", "cache/item"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("disconnect erased private settings", err)
		}
	}
	if err := connector.RequireConfigured(pkg.PersistentRoot(), pkg.ID); err == nil {
		t.Fatal("disconnected connector admitted")
	}
}
func TestBuiltinConfigurationIsNotForced(t *testing.T) {
	pkg := connector.Package{Manifest: connector.Manifest{ID: "builtin.example"}, Builtin: true, StateRoot: t.TempDir()}
	state, err := pkg.ReadConnection()
	if err != nil || state.Configured {
		t.Fatal(state, err)
	}
	if err := connector.RequireConfigured(pkg.PersistentRoot(), pkg.ID); err == nil {
		t.Fatal("builtin bypassed completion check")
	}
	if _, err := pkg.SetConfigured(true); err != nil {
		t.Fatal(err)
	}
	if err := connector.RequireConfigured(pkg.PersistentRoot(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pkg.SetConfigured(false); err != nil {
		t.Fatal(err)
	}
	if err := connector.RequireConfigured(pkg.PersistentRoot(), pkg.ID); err == nil {
		t.Fatal("builtin remained configured")
	}
}
