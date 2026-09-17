package connectorauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/connector"
	"golang.org/x/oauth2"
)

func TestIndependentGrantsAndLogoutFence(t *testing.T) {
	root := t.TempDir()
	for _, target := range []string{"https://a.example/mcp", "https://b.example/mcp"} {
		if err := saveCredential(root, "demo", oauthCredential{Resource: target, Destination: target, Token: &oauth2.Token{AccessToken: target}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{"https://a.example/mcp", "https://b.example/mcp"} {
		value, err := AccessToken(t.Context(), root, "demo", target, nil, target)
		if err != nil || value != target {
			t.Fatal("grant was overwritten", err)
		}
	}
	if _, err := AccessToken(t.Context(), root, "demo", "https://a.example/mcp", nil, "https://b.example/mcp"); err == nil {
		t.Fatal("cross-resource token accepted")
	}
	if err := changeAuthState(root, "demo", true); err != nil {
		t.Fatal(err)
	}
	if CredentialReady(root, "demo", "https://a.example/mcp") {
		t.Fatal("logout fence ignored")
	}
}

func TestClientMetadataRequiresMatchingRegisteredCallback(t *testing.T) {
	redirect := "http://127.0.0.1:8765/callback"
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"client_id": server.URL + "/client.json", "client_name": "Platform", "redirect_uris": []string{redirect}})
	}))
	defer server.Close()
	if err := validateClientMetadata(t.Context(), server.Client(), server.URL+"/client.json", redirect); err != nil {
		t.Fatal(err)
	}
	if err := validateClientMetadata(t.Context(), server.Client(), server.URL+"/client.json", "http://127.0.0.1:9999/callback"); err == nil {
		t.Fatal("unregistered callback accepted")
	}
}

func TestMCPRejectRefreshAndScopeChallenge(t *testing.T) {
	root := t.TempDir()
	var refreshes atomic.Int32
	var calls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			r.ParseForm()
			if r.Form.Get("resource") != server.URL+"/mcp" {
				t.Error("refresh lacks resource")
			}
			refreshes.Add(1)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"access_token":"new","refresh_token":"rotated","token_type":"Bearer","expires_in":3600}`)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer old" {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new" {
			t.Error("incorrect authorization")
		}
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="documents.write"`)
		w.WriteHeader(403)
	}))
	defer server.Close()
	resource := server.URL + "/mcp"
	c := oauthCredential{MCP: true, Resource: resource, Destination: resource, Config: oauth2.Config{ClientID: "test", Endpoint: oauth2.Endpoint{TokenURL: server.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}, Token: &oauth2.Token{AccessToken: "old", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}}
	if err := saveCredential(root, "demo", c); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: AuthorizingTransport{Base: server.Client().Transport, Client: server.Client(), Root: root, ID: "demo", Resource: resource}}
	response, err := client.Post(resource, "application/json", strings.NewReader(`{"method":"tools/call"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 || refreshes.Load() != 1 || calls.Load() != 2 {
		t.Fatal("unbounded or missing auth recovery")
	}
	if CredentialReady(root, "demo", resource, resource) {
		t.Fatal("scope challenge did not require authorization")
	}
	saved, err := readCredential(root, "demo")
	if err != nil || len(saved.RequiredScopes) != 1 || saved.RequiredScopes[0] != "documents.write" {
		t.Fatal("missing scope challenge")
	}
}

func TestDiscoveryFallsBackToRootMetadata(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mcp":
			w.WriteHeader(401)
		case "/.well-known/oauth-protected-resource/mcp":
			http.NotFound(w, r)
		case "/.well-known/oauth-protected-resource":
			json.NewEncoder(w).Encode(map[string]any{"resource": server.URL + "/mcp", "authorization_servers": []string{server.URL}})
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "code_challenge_methods_supported": []string{"S256"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := New(t.Context(), connector.Sources{ExternalRoot: t.TempDir()}, nil)
	prm, _, err := m.discover(t.Context(), server.URL+"/mcp", server.URL+"/mcp", oauthSettings{ClientID: "test"})
	if err != nil || prm == nil {
		t.Fatal(err)
	}
}

func TestTokenEnvironmentReadsLatestValueAndPreservesSources(t *testing.T) {
	root := t.TempDir()
	writeAuthPackage(t, root, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "API_KEY", "required": true}}}}, map[string]any{"type": "http", "url": "https://example.com/mcp"})
	sources := connector.Sources{ExternalRoot: root}
	m := New(t.Context(), sources, nil)
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "demo", "mcp.json")
	before, _ := os.ReadFile(path)
	d := connector.CredentialEnvironment{Root: sources.PersistentRoot(), ID: "demo", Mode: connector.AuthToken, Env: map[string]string{"SERVICE_TOKEN": "${API_KEY}"}}
	for _, value := range []string{"first", "second"} {
		if _, err := m.SetToken(t.Context(), "demo", map[string]string{"API_KEY": value}); err != nil {
			t.Fatal(err)
		}
		env, err := ResolveEnvironment(context.Background(), d, "")
		if err != nil || env["SERVICE_TOKEN"] != value {
			t.Fatal("stale environment", err)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("MCP source modified")
	}
	if err := m.Logout(t.Context(), pkg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveEnvironment(t.Context(), d, ""); err == nil {
		t.Fatal("logged-out credentials accepted")
	}
}

func TestComponentAuthorizationAndLateLoginAfterLogout(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") == "late" {
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": r.Form.Get("code"), "token_type": "Bearer", "expires_in": 3600})
	}))
	defer server.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	os.MkdirAll(dir, 0755)
	settings := func(name string) map[string]any {
		return map[string]any{"client_id": "test", "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "resource": server.URL + "/" + name}
	}
	manifest := map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "oauth", "oauth": settings("a"), "auth_bindings": map[string]any{"mcp:b": map[string]any{"oauth": settings("b")}}}
	for name, value := range map[string]any{"connector.json": manifest, "mcp.json": map[string]any{"mcpServers": map[string]any{"a": map[string]any{"type": "http", "url": server.URL + "/a"}, "b": map[string]any{"type": "http", "url": server.URL + "/b"}}}} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	sources := connector.Sources{ExternalRoot: root}
	m := New(t.Context(), sources, nil)
	if _, err := m.Start("demo"); err == nil {
		t.Fatal("ambiguous component accepted")
	}
	callback := func(code string) {
		s := waitStatus(t, m, "demo", "pending")
		u, _ := url.Parse(s.URL)
		response, err := http.Get(u.Query().Get("redirect_uri") + "?" + url.Values{"state": {u.Query().Get("state")}, "code": {code}}.Encode())
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	for _, name := range []string{"a", "b"} {
		if _, err := m.StartComponent("demo", name); err != nil {
			t.Fatal(err)
		}
		callback(name)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			status, err := m.StatusComponent(t.Context(), "demo", name)
			if err != nil {
				t.Fatal(err)
			}
			if status.Status == "authorized" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		value, err := AccessToken(t.Context(), sources.PersistentRoot(), "demo", server.URL+"/"+name, nil, server.URL+"/"+name)
		if err != nil || value != name {
			t.Fatal("wrong component grant", err)
		}
	}
	for _, name := range []string{"a", "b"} {
		if !CredentialReady(sources.PersistentRoot(), "demo", server.URL+"/"+name, server.URL+"/"+name) {
			t.Fatal("other grant lost")
		}
	}
	if _, err := m.StartComponent("demo", "a"); err != nil {
		t.Fatal(err)
	}
	callback("late")
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("token exchange not started")
	}
	other := New(t.Context(), sources, nil)
	if err := other.Logout(t.Context(), "demo"); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, _ := m.StatusComponent(t.Context(), "demo", "a")
		if status.Status == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if CredentialReady(sources.PersistentRoot(), "demo", server.URL+"/a") {
		t.Fatal("late login recreated credentials")
	}
}
