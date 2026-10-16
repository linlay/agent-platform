package connectorauth

import (
	"context"
	"encoding/json"
	"net/http"
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
			upstream := newMCPAuthUpstream(t, func(u *mcpAuthUpstream) {
				u.token = func(w http.ResponseWriter, r *http.Request) {
					defer close(exchangeDone)
					close(entered)
					<-release
					json.NewEncoder(w).Encode(map[string]any{"access_token": "late-secret", "token_type": "Bearer", "expires_in": 3600})
				}
			})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			sources := connector.Sources{ExternalRoot: t.TempDir(), StateRoot: t.TempDir()}
			pkg := upstream.writePackage(t, sources.ExternalRoot, nil)
			pkg.StateRoot = sources.StateRoot
			manager := New(t.Context(), sources, nil)
			manager.client = upstream.Client()
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
