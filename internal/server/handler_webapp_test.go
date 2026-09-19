package server

import (
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/webapp"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopConnectorAuthReusesExistingState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"connector.json": `{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"token","token_schema":{"fields":[{"key":"KEY","type":"password","required":true}]}}`,
		"mcp.json":       `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp","headers":{"X-Key":"${KEY}"}}}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	state := t.TempDir()
	manager := connectorauth.New(context.Background(), connector.Sources{ExternalRoot: root, StateRoot: state}, nil)
	if _, err := manager.SetToken(context.Background(), "demo", map[string]string{"KEY": "fixture"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{connectorAuth: manager}
	principal := &Principal{Subject: "desktop-user:" + strings.Repeat("a", 64), Claims: map[string]any{"scope": "app", "device_id": "device"}}
	call := func(method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/desktop/connector/auth?id=demo", nil)
		r = r.WithContext(WithPrincipal(r.Context(), principal))
		w := httptest.NewRecorder()
		s.handleDesktopConnectorAuth(w, r)
		return w
	}
	if w := call("GET"); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"authorized"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("DELETE"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	status, err := manager.Status(context.Background(), "demo")
	if err != nil || status.Status != "unauthorized" {
		t.Fatal("logout did not update existing manager", status, err)
	}
	if _, err := os.Stat(filepath.Join(state, "users")); !os.IsNotExist(err) {
		t.Fatal("unexpected per-user credentials", err)
	}
}

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
