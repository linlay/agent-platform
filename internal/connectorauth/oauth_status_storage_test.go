package connectorauth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
)

func TestOAuthStatusSeparatesMissingGrantFromDamagedStorage(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "state")}
	writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "oauth",
		"oauth": map[string]any{"client_id": "fixture", "authorization_endpoint": "https://example.test/authorize", "token_endpoint": "https://example.test/token", "resource": "https://example.test/mcp"}},
		map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"})
	m := New(t.Context(), sources, nil)
	status, err := m.StatusComponent(t.Context(), "demo", "")
	if err != nil || status.Status != "unauthorized" {
		t.Fatal("missing grant must require sign-in", status, err)
	}
	path, err := credentialPath(sources.PersistentRoot(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StatusComponent(t.Context(), "demo", ""); !errors.Is(err, ErrOAuthCredentialUnavailable) {
		t.Fatal("damaged storage was reported as an authorization state", err)
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pkg.SetConfigured(true); err != nil {
		t.Fatal(err)
	}
	if connection, err := m.Connection(t.Context(), "demo"); err != nil || connection.Readiness != "unavailable" {
		t.Fatal("damaged storage must not ask for sign-in", connection.Readiness, err)
	}
}
