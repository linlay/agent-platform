package connectorauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/connector"
)

func TestOfflineTokenCandidatePreservesActiveCredentialsAndListsLocally(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "KEY", "required": true}}}}, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"})
	var calls atomic.Int32
	var offline atomic.Bool
	m := New(t.Context(), sources, nil).WithCredentialValidator(func(context.Context, connector.Package, map[string]string) error {
		calls.Add(1)
		if offline.Load() {
			return errors.New("network unavailable")
		}
		return nil
	})
	pkg, _ := sources.Load("demo")
	if _, err := m.SetToken(t.Context(), "demo", map[string]string{"KEY": "old-value"}); err != nil {
		t.Fatal(err)
	}
	offline.Store(true)
	got, err := m.SetToken(t.Context(), "demo", map[string]string{"KEY": "new-value"})
	if err != nil || got.Status != "pending_verification" || !got.PendingVerification {
		t.Fatal(got, err)
	}
	values, ready, err := TokenValues(pkg)
	if err != nil || !ready || values["KEY"] != "old-value" {
		t.Fatal("candidate replaced active credentials", err)
	}
	before := calls.Load()
	for i := 0; i < 3; i++ {
		c, err := m.Connection(t.Context(), "demo")
		if err != nil || !c.Configured || !c.Authentication.PendingVerification || c.Authentication.Status != "authorized" {
			t.Fatal(c, err)
		}
	}
	if calls.Load() != before {
		t.Fatal("list performed a remote probe")
	}
	restarted := New(t.Context(), sources, nil)
	if c, err := restarted.Connection(t.Context(), "demo"); err != nil || c.Authentication.Status != "authorized" || !c.Authentication.PendingVerification {
		t.Fatal("restart lost verification", c, err)
	}
	offline.Store(false)
	got, err = m.Check(t.Context(), "demo", "")
	if err != nil || got.Status != "authorized" {
		t.Fatal(got, err)
	}
	values, ready, err = TokenValues(pkg)
	if err != nil || !ready || values["KEY"] != "new-value" {
		t.Fatal("check did not publish candidate", err)
	}
	if _, pending, err := pendingTokenValues(pkg); err != nil || pending {
		t.Fatal("candidate retained", err)
	}
	// Local logout clears active/pending credentials and cached verification only.
	offline.Store(true)
	if _, err = m.SetToken(t.Context(), "demo", map[string]string{"KEY": "later"}); err != nil {
		t.Fatal(err)
	}
	dir, _ := pkg.ConnectorStateDir()
	for _, name := range []string{"credentials.json", "pending-credentials.json", "verification.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("nonprivate file", name, err)
		}
	}
	if _, err = m.Disconnect(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"credentials.json", "pending-credentials.json", "verification.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal("logout retained", name, err)
		}
	}
}
func TestFirstOfflineTokenCanBeSavedWithoutBecomingAuthorized(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
	m := New(t.Context(), sources, nil)
	root := sources.ExternalRoot
	writeAuthPackage(t, root, "remote", map[string]any{"id": "remote", "name": "Remote", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "KEY", "required": true}}}}, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"})
	m.WithCredentialValidator(func(context.Context, connector.Package, map[string]string) error { return errors.New("offline") })
	got, err := m.SetToken(t.Context(), "remote", map[string]string{"KEY": "candidate"})
	if err != nil || got.Status != "pending_verification" {
		t.Fatal(got, err)
	}
	c, err := m.Connection(t.Context(), "remote")
	if err != nil || c.Authentication.Status != "pending_verification" || c.Readiness == "ready" {
		t.Fatal(c, err)
	}
}
func TestLegacyCLILogoutDoesNotDeleteConfiguration(t *testing.T) {
	m, pkg := tokenCLIFixture(t, false)
	pkg.CLI["platform"] = map[string]any{"command": "demo", "configEnv": "DEMO_CONFIG_DIR", "logoutMode": "delete-config"}
	dir, _ := pkg.ConnectorStateDir()
	file := filepath.Join(dir, "config", "settings")
	os.MkdirAll(filepath.Dir(file), 0700)
	os.WriteFile(file, []byte("keep"), 0600)
	err := m.logoutCLI(t.Context(), pkg)
	if err == nil || !strings.Contains(err.Error(), "reset configuration separately") {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
		t.Fatal("settings deleted", err)
	}
}
