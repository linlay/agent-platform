package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPersonalCredentialsNeverFallBackOrCrossOwners(t *testing.T) {
	ctx := context.Background()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "connectors")}
	writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "API_KEY", "type": "password", "required": true}}}}, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp", "headers": map[string]any{"X-API-Key": "${API_KEY}"}})
	host := New(ctx, sources, nil)
	if _, err := host.SetToken(ctx, "demo", map[string]string{"API_KEY": "deployment"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := host.Personal("alice", "default")
	bob, _ := host.Personal("bob", "default")
	a2, _ := host.Personal("alice", "default")
	if alice != a2 {
		t.Fatal("personal sessions not shared")
	}
	for _, manager := range []*Manager{alice, bob} {
		status, err := manager.Status(ctx, "demo")
		if err != nil || status.Status != "unauthorized" {
			t.Fatal("deployment credentials leaked", status, err)
		}
	}
	if _, err := alice.SetToken(ctx, "demo", map[string]string{"API_KEY": "alice"}); err != nil {
		t.Fatal(err)
	}
	status, err := bob.Status(ctx, "demo")
	if err != nil || status.Status != "unauthorized" {
		t.Fatal("cross-owner credentials", status, err)
	}
	before, _ := alice.Revision("demo")
	if err := alice.Logout(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	after, _ := alice.Revision("demo")
	if before == after {
		t.Fatal("logout did not fence results")
	}
	status, err = host.Status(ctx, "demo")
	if err != nil || status.Status != "authorized" {
		t.Fatal("personal logout changed deployment")
	}
	other, _ := host.Personal("alice", "secondary")
	if other == alice || other.sources.PersistentRoot() == alice.sources.PersistentRoot() {
		t.Fatal("bindings not isolated")
	}
}
func TestPersonalRejectsInvalidIdentityAndSymlink(t *testing.T) {
	root := t.TempDir()
	m := New(context.Background(), connector.Sources{StateRoot: root}, nil)
	if _, err := m.Personal("", "default"); err == nil {
		t.Fatal("empty owner")
	}
	if _, err := m.Personal("alice", "../default"); err == nil {
		t.Fatal("binding escape")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "users")); err != nil {
		t.Skip(err)
	}
	if _, err := m.Personal("alice", "default"); err == nil {
		t.Fatal("symlink accepted")
	}
}
