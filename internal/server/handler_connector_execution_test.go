package server

import (
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/connectorops"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustedConnectorAuthReusesExistingState(t *testing.T) {
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
	manager := connectorauth.New(context.Background(), connector.Sources{ExternalRoot: root, StateRoot: state}, nil).WithCredentialValidator(func(context.Context, connector.Package, map[string]string) error { return nil })
	if _, err := manager.SetToken(context.Background(), "demo", map[string]string{"KEY": "fixture"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{connectorAuth: manager}
	principal := &Principal{Subject: "desktop-app", Claims: map[string]any{"scope": "app", "device_id": "device"}}
	call := func(method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/connectors/auth?id=demo", nil)
		r = r.WithContext(WithPrincipal(r.Context(), principal))
		w := httptest.NewRecorder()
		s.handleTrustedConnectorAuth(w, r)
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

func TestConnectorExecutionGrantRequiresAuthorityAndCannotSelectOwner(t *testing.T) {
	server := &Server{connectorGrants: connectorops.NewGrants(context.Background())}
	call := func(p *Principal, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/connectors/execution/grants", strings.NewReader(body))
		if p != nil {
			r = r.WithContext(WithPrincipal(r.Context(), p))
		}
		w := httptest.NewRecorder()
		server.handleConnectorExecutionGrant(w, r)
		return w
	}
	for _, p := range []*Principal{nil, {Subject: "", Claims: map[string]any{"scope": "app", "device_id": "device"}}, {Subject: "desktop-app", Claims: map[string]any{"scope": "app"}}, {Subject: "desktop-app", Claims: map[string]any{"scope": "user", "device_id": "device"}}} {
		if w := call(p, `{"idempotencyNamespace":"calendar","operations":{}}`); w.Code != 403 {
			t.Fatal("accepted unauthenticated or non-app host", w.Body.String())
		}
	}
	principal := &Principal{Subject: "desktop-app", Claims: map[string]any{"scope": "app", "device_id": "device"}}
	if w := call(principal, `{"idempotencyNamespace":"calendar","subject":"bob","operations":{}}`); w.Code != 400 {
		t.Fatal("owner accepted from payload")
	}
	if w := call(principal, `{"version":3,"idempotencyNamespace":"calendar","execution":[{"connectorId":"wecom","adapter":"cli"}]}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestConnectorExecutionRejectsMissingOrRevokedGrant(t *testing.T) {
	server := &Server{connectorGrants: connectorops.NewGrants(context.Background())}
	grant, _ := server.connectorGrants.Issue("alice", "calendar", nil)
	server.connectorGrants.Revoke("alice", grant.ID)
	for _, auth := range []string{"", "Bearer ordinary-jwt", "Bearer " + grant.Token} {
		r := httptest.NewRequest(http.MethodPost, "/api/connectors/execution/list", strings.NewReader(`{}`))
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		server.handleConnectorExecution(w, r)
		if w.Code != 403 {
			t.Fatal("accepted missing or revoked grant")
		}
	}
}

func TestConnectorExecutionGrantRejectsApplicationModel(t *testing.T) {
	s := &Server{connectorGrants: connectorops.NewGrants(context.Background())}
	for _, body := range []string{
		`{"version":2,"idempotencyNamespace":"stable","execution":[]}`,
		`{"version":3,"idempotencyNamespace":"stable","execution":[],"appId":"app"}`,
		`{"version":3,"idempotencyNamespace":"stable","execution":[],"chatIds":["chat"]}`,
		`{"version":3,"idempotencyNamespace":"stable","execution":[],"subject":"other"}`,
	} {
		r := httptest.NewRequest("POST", "/api/connectors/execution/grants", strings.NewReader(body))
		r = r.WithContext(WithPrincipal(r.Context(), &Principal{Subject: "authority", Claims: map[string]any{"scope": "app", "device_id": "device"}}))
		w := httptest.NewRecorder()
		s.handleConnectorExecutionGrant(w, r)
		if w.Code != 400 && w.Code != 409 {
			t.Fatalf("accepted incompatible payload: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestExecutionAuthenticationRouting(t *testing.T) {
	s := &Server{router: http.NewServeMux(), authVerifier: NewJWTVerifier(config.AuthConfig{}), connectorGrants: connectorops.NewGrants(t.Context())} // Auth.Enabled=false must not bypass authentication.
	s.routes()
	for _, path := range []string{"/api/connectors/execution/grants", "/api/connectors/auth", "/api/connectors/auth/cancel", "/api/chat/artifacts/list", "/api/chat/artifacts/get", "/api/chat/artifacts/read"} {
		for _, authorization := range []string{"", "Bearer cxg_restricted", "Bearer wap_retired"} {
			r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
			r.Header.Set("Authorization", authorization)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("anonymous authority access: %s %d", path, w.Code)
			}
		}
	}
	for _, path := range []string{"/api/webapp/connector/invoke", "/api/webapp/artifact/read", "/api/desktop/webapp/grants", "/api/desktop/connector/auth"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 410 || !strings.Contains(w.Body.String(), "connector_contract_upgrade_required") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/connectors/execution/list", "/api/connectors/execution/describe", "/api/connectors/execution/invoke"} {
		for _, authorization := range []string{"", "Bearer ordinary-jwt", "Bearer wap_retired", "Bearer cxg_unknown"} {
			r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
			r.Header.Set("Authorization", authorization)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("execution accepted non-capability credential: %s %d", path, w.Code)
			}
		}
	}
}
