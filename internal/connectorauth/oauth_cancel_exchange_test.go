package connectorauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"agent-platform/internal/connector"
)

func TestOAuthCancellationDuringTokenExchangeCannotRestoreConnection(t *testing.T) {
	for _, operation := range []string{"cancel", "disconnect"} {
		t.Run(operation, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			exchangeDone := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/token" {
					http.NotFound(w, r)
					return
				}
				defer close(exchangeDone)
				close(entered)
				<-release
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"access_token": "late-secret", "token_type": "Bearer", "expires_in": 3600})
			}))
			defer upstream.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
			pkg := writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{
				"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "oauth",
				"oauth": map[string]any{"client_id": "public-client", "authorization_endpoint": upstream.URL + "/authorize", "token_endpoint": upstream.URL + "/token", "resource": upstream.URL + "/mcp"},
			}, map[string]any{"type": "streamableHttp", "url": upstream.URL + "/mcp"})
			pkg.StateRoot = sources.StateRoot
			manager := New(t.Context(), sources, nil)
			if _, err := manager.Start("demo"); err != nil {
				t.Fatal(err)
			}
			session := waitStatus(t, manager, "demo", "pending")
			authorize, err := url.Parse(session.URL)
			if err != nil {
				t.Fatal(err)
			}
			callbackDone := make(chan struct{})
			go func() {
				defer close(callbackDone)
				response, err := http.Get(authorize.Query().Get("redirect_uri") + "?" + url.Values{"state": {authorize.Query().Get("state")}, "code": {"late-code"}}.Encode())
				if err == nil {
					response.Body.Close()
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("token exchange did not start")
			}
			if operation == "cancel" {
				if err := manager.CancelSession("demo", session.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := manager.Disconnect(context.Background(), "demo"); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-callbackDone:
			case <-time.After(3 * time.Second):
				t.Fatal("callback did not finish after cancel")
			}
			close(release)
			select {
			case <-exchangeDone:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream did not emit late response")
			}
			if CredentialReady(sources.PersistentRoot(), "demo", upstream.URL+"/mcp") {
				t.Fatal("canceled exchange persisted credentials")
			}
			state, err := pkg.ReadConnection()
			if err != nil || state.Configured {
				t.Fatal("canceled exchange restored connection", state, err)
			}
		})
	}
}
