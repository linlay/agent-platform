package connectorauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/connector"
)

func TestOneIDStatusFollowsDesktopWithoutPersistingCredentials(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: filepath.Join(t.TempDir(), "connectors")}
	writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": "oneid-token"}, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"})
	ssoIdTokenFile := filepath.Join(t.TempDir(), "sso-id-token.txt")
	m := New(t.Context(), sources, nil).WithSsoIdTokenFile(ssoIdTokenFile)
	check := func(want string) {
		t.Helper()
		s, err := m.Status(t.Context(), "demo")
		if err != nil || s.Status != want {
			t.Fatal(s, err)
		}
		b, _ := json.Marshal(s)
		if strings.Contains(string(b), "private-token") {
			t.Fatal("status exposed SSO token")
		}
	}
	check("unauthorized")
	os.WriteFile(ssoIdTokenFile, []byte("private-token-a\n"), 0600)
	check("authorized")
	if _, err := m.Start("demo"); err == nil {
		t.Fatal("oneid started connector login")
	}
	if _, err := m.SetToken(t.Context(), "demo", map[string]string{"TOKEN": "manual"}); err == nil {
		t.Fatal("oneid accepted manual token")
	}
	if err := m.Logout(t.Context(), "demo"); err == nil {
		t.Fatal("connector logout accepted ownership of Desktop SSO")
	}
	data, _ := os.ReadFile(ssoIdTokenFile)
	if string(data) != "private-token-a\n" {
		t.Fatal("modified Desktop identity")
	}
	os.WriteFile(ssoIdTokenFile, []byte("private-token-b\n"), 0600)
	check("authorized")
	os.WriteFile(ssoIdTokenFile, []byte("invalid\ntoken"), 0600)
	check("unauthorized")
	os.Remove(ssoIdTokenFile)
	check("unauthorized")
	if _, err := os.Stat(filepath.Join(sources.StateRoot, "demo")); !os.IsNotExist(err) {
		t.Fatal("oneid created connector credential state")
	}
}
