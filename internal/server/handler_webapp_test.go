package server

import (
	"agent-platform/internal/webapp"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebappGrantRequiresPersonalDesktopAndCannotSelectOwner(t *testing.T) {
	server := &Server{webappGrants: webapp.NewGrants(context.Background())}
	call := func(p *Principal, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/desktop/webapp/grants", strings.NewReader(body))
		if p != nil {
			r = r.WithContext(WithPrincipal(r.Context(), p))
		}
		w := httptest.NewRecorder()
		server.handleWebappGrant(w, r)
		return w
	}
	for _, p := range []*Principal{nil, {Subject: "service", Claims: map[string]any{"scope": "app", "device_id": "device"}}, {Subject: "desktop-user:" + strings.Repeat("a", 64), Claims: map[string]any{"scope": "user", "device_id": "device"}}} {
		if w := call(p, `{"appId":"calendar","operations":{}}`); w.Code != 403 {
			t.Fatal("accepted non-personal host", w.Body.String())
		}
	}
	principal := &Principal{Subject: "desktop-user:" + strings.Repeat("a", 64), Claims: map[string]any{"scope": "app", "device_id": "device"}}
	if w := call(principal, `{"appId":"calendar","subject":"bob","operations":{}}`); w.Code != 400 {
		t.Fatal("owner accepted from payload")
	}
	if w := call(principal, `{"appId":"calendar","operations":{"wecom":["meetings.list"]}}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestWebappConnectorRejectsMissingOrRevokedGrant(t *testing.T) {
	server := &Server{webappGrants: webapp.NewGrants(context.Background())}
	grant, _ := server.webappGrants.Issue("alice", "calendar", nil)
	server.webappGrants.Revoke("alice", grant.ID)
	for _, auth := range []string{"", "Bearer ordinary-jwt", "Bearer " + grant.Token} {
		r := httptest.NewRequest(http.MethodPost, "/api/webapp/connector/list", strings.NewReader(`{}`))
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		server.handleWebappConnector(w, r)
		if w.Code != 403 {
			t.Fatal("accepted missing or revoked grant")
		}
	}
}
