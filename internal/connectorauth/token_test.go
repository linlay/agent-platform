package connectorauth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/connector"
)

func TestManualTokenPersistenceValidationAndLogout(t *testing.T) {
	ctx := context.Background()
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "connectors")}
	writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{
		"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token",
		"token_schema": map[string]any{"fields": []map[string]any{{"key": "API_KEY", "type": "password", "required": true}}},
	}, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp", "headers": map[string]any{"X-API-Key": "${API_KEY}"}})
	reloads := 0
	m := New(ctx, sources, func(context.Context, string) error { reloads++; return nil })
	status, err := m.Status(ctx, "demo")
	if err != nil || status.Status != "unauthorized" {
		t.Fatal(status, err)
	}
	if _, err = m.Start("demo"); err == nil {
		t.Fatal("token started interactive login")
	}
	for _, bad := range []map[string]string{nil, {"API_KEY": ""}, {"API_KEY": "bad\nvalue"}, {"API_KEY": "private-key", "EXTRA": "private-value"}} {
		if _, err = m.SetToken(ctx, "demo", bad); err == nil || strings.Contains(err.Error(), "private-") {
			t.Fatal("invalid credential validation", err)
		}
	}
	status, err = m.SetToken(ctx, "demo", map[string]string{"API_KEY": "private-value-$()"})
	if err != nil || status.Status != "authorized" || reloads != 1 {
		t.Fatal(status, err, reloads)
	}
	b, _ := json.Marshal(status)
	if strings.Contains(string(b), "private-value") {
		t.Fatal("status leaked token")
	}
	p, _ := connector.CredentialsPath(sources.PersistentRoot(), "demo")
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credentials not private", err)
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	values, ready, err := TokenValues(pkg)
	if err != nil || !ready || values["API_KEY"] != "private-value-$()" {
		t.Fatal("credential storage failed", err)
	}
	status, err = New(ctx, sources, nil).Status(ctx, "demo")
	if err != nil || status.Status != "authorized" {
		t.Fatal("restart lost credentials", err)
	}
	if _, err = os.Stat(filepath.Join(pkg.Dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("credentials in package")
	}
	if err = os.Remove(p); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "preserved")
	os.WriteFile(target, []byte("preserved"), 0600)
	if err = os.Symlink(target, p); err == nil {
		if _, err = m.SetToken(ctx, "demo", map[string]string{"API_KEY": "new"}); err == nil {
			t.Fatal("accepted credential symlink")
		}
		data, _ := os.ReadFile(target)
		if string(data) != "preserved" {
			t.Fatal("changed symlink target")
		}
		os.Remove(p)
	}
	if _, err = m.SetToken(ctx, "demo", map[string]string{"API_KEY": "new"}); err != nil {
		t.Fatal(err)
	}
	if err = m.Logout(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	status, err = m.Status(ctx, "demo")
	if err != nil || status.Status != "unauthorized" {
		t.Fatal("logout retained token", err)
	}
	if _, err = os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("credential file retained")
	}
}
