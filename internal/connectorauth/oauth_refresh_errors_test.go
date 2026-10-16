package connectorauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/connector"
	"golang.org/x/oauth2"
)

func TestRefreshSeparatesClientConfigurationAndAuthorizationFailures(t *testing.T) {
	for _, test := range []struct {
		name          string
		code          string
		want          error
		requiresLogin bool
	}{
		{"client_configuration", "invalid_client", ErrOAuthClientSetupRequired, false},
		{"authorization_revoked", "invalid_grant", ErrOAuthAuthorizationRequired, true},
		{"temporary_service_failure", "temporarily_unavailable", ErrOAuthRefreshFailed, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": test.code, "error_description": "private-upstream-detail"})
			}))
			defer upstream.Close()
			root := t.TempDir()
			resource := "https://example.test/mcp"
			credential := oauthCredential{
				MCP:      true,
				Resource: resource, Destination: resource,
				Config: oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{TokenURL: upstream.URL, AuthStyle: oauth2.AuthStyleInParams}},
				Token:  &oauth2.Token{AccessToken: "old-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Hour)},
			}
			if err := saveCredential(root, "demo", credential); err != nil {
				t.Fatal(err)
			}
			pkg := connector.Package{Manifest: connector.Manifest{ID: "demo", AuthMode: connector.AuthMCP}, StateRoot: root}
			if _, err := pkg.SetConfigured(true); err != nil {
				t.Fatal(err)
			}
			_, err := AccessToken(t.Context(), root, "demo", resource, upstream.Client(), resource)
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "private-upstream-detail") || strings.Contains(err.Error(), "old-refresh") {
				t.Fatal("wrong failure classification or sensitive diagnostic", err)
			}
			saved, err := readCredential(root, "demo")
			if err != nil || saved.Token == nil || saved.Token.AccessToken != credential.Token.AccessToken || saved.Token.RefreshToken != credential.Token.RefreshToken || saved.RequiresLogin != test.requiresLogin {
				t.Fatal("refresh failure discarded authorization materials", err)
			}
			state, err := pkg.ReadConnection()
			if err != nil || !state.Configured {
				t.Fatal("refresh failure changed binding preference", state, err)
			}
			if got := CredentialReady(root, "demo", resource, resource); got == test.requiresLogin {
				t.Fatal("only explicit invalid_grant should require authorization", got)
			}
		})
	}
}

func TestOAuthDamagedStorageDoesNotAskUserToAuthorizeAgain(t *testing.T) {
	root := t.TempDir()
	resource := "https://example.test/mcp"
	credential := oauthCredential{MCP: true, Resource: resource, Destination: resource, Token: &oauth2.Token{AccessToken: "saved-access", RefreshToken: "saved-refresh", Expiry: time.Now().Add(time.Hour)}}
	if err := saveCredential(root, "demo", credential); err != nil {
		t.Fatal(err)
	}
	dir, err := StateDir(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth-state.json"), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := AccessToken(t.Context(), root, "demo", resource, nil, resource); !errors.Is(err, ErrOAuthCredentialUnavailable) {
		t.Fatal("storage failure was treated as lost authorization", err)
	}
	saved, err := readCredential(root, "demo")
	if err != nil || saved.RequiresLogin || saved.Token == nil || saved.Token.AccessToken != credential.Token.AccessToken || saved.Token.RefreshToken != credential.Token.RefreshToken {
		t.Fatal("storage failure modified saved authorization", err)
	}
}
