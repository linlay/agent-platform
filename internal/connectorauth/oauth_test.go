package connectorauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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

func writeAuthPackage(t *testing.T, root, id string, manifest map[string]any, component map[string]any) connector.Package {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"connector.json": manifest, "mcp.json": map[string]any{"mcpServers": map[string]any{"main": component}}} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := connector.Load(root, id)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestCredentialSnapshotDoesNotWaitForRefreshLock(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	unlock, err := lockCredentials(ctx, root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := saveCredential(root, "demo", oauthCredential{Resource: "https://example.test/mcp", Token: &oauth2.Token{AccessToken: "expired", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() { ready <- CredentialReady(root, "demo", "https://example.test/mcp") }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("valid refresh credential unavailable")
		}
	case <-time.After(time.Second):
		t.Fatal("local snapshot blocked on refresh lock")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if second, err := lockCredentials(waitCtx, root, "demo"); err == nil {
		second()
		t.Fatal("concurrent credential writer acquired the lock")
	}
	other, err := lockCredentials(waitCtx, root, "other")
	// Use a fresh context after the first waiter expired.
	if err != nil {
		other, err = lockCredentials(ctx, root, "other")
	}
	if err != nil {
		t.Fatal(err)
	}
	other()
}

func TestOAuthRejectsConflictingAuthenticationDeclarations(t *testing.T) {
	for _, component := range []map[string]any{
		{"type": "streamableHttp", "url": "https://example.test/mcp", "headers": map[string]any{"authorization": "secret"}},
		{"type": "streamableHttp", "url": "https://example.test/mcp", "platform": map[string]any{"authSource": "identity-file"}},
		{"type": "stdio", "command": "some-server"},
	} {
		pkg := connector.Package{Manifest: connector.Manifest{AuthMode: "oauth"}, MCP: map[string]map[string]any{"main": component}}
		if err := ValidatePackage(pkg); err == nil {
			t.Fatal("accepted incompatible OAuth configuration")
		}
	}
}

func waitStatus(t *testing.T, m *Manager, id, want string) Session {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		s := m.sessions[id].Session
		m.mu.Unlock()
		if s.Status == want {
			return s
		}
		if s.Status == "failed" {
			t.Fatalf("login failed: %s", s.Message)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("login status timeout")
	return Session{}
}

func TestOAuthPKCEPersistenceRefreshLogoutAndDestinationBinding(t *testing.T) {
	var upstream *httptest.Server
	var challenge, redirect string
	var refreshCount atomic.Int32
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/resource-metadata":
			json.NewEncoder(w).Encode(map[string]any{"resource": upstream.URL + "/mcp", "authorization_servers": []string{upstream.URL}})
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]any{"issuer": upstream.URL, "authorization_endpoint": upstream.URL + "/authorize", "token_endpoint": upstream.URL + "/token", "registration_endpoint": upstream.URL + "/register", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"}})
		case "/register":
			var reg map[string]any
			json.NewDecoder(r.Body).Decode(&reg)
			reg["client_id"] = "test-client"
			json.NewEncoder(w).Encode(reg)
		case "/token":
			r.ParseForm()
			if r.Form.Get("grant_type") == "refresh_token" {
				refreshCount.Add(1)
				json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed-secret", "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 3600})
				return
			}
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.Form.Get("code") != "test-code" || r.Form.Get("resource") != upstream.URL+"/mcp" || r.Form.Get("redirect_uri") != redirect {
				t.Error("invalid PKCE exchange")
				http.Error(w, "bad grant", 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "private-token", "refresh_token": "private-refresh", "token_type": "Bearer", "expires_in": 3600})
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer refreshed-secret" {
				t.Error("missing refreshed token")
			}
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	root := t.TempDir()
	stateRoot := (connector.Sources{ExternalRoot: root}).PersistentRoot()
	writeAuthPackage(t, root, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "mcp", "oauth": map[string]any{"discovery": true, "resourceMetadataUrl": upstream.URL + "/resource-metadata"}}, map[string]any{"type": "streamableHttp", "url": upstream.URL + "/mcp"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, connector.Sources{ExternalRoot: root}, nil)
	first, err := m.Start("demo")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Start("demo")
	if err != nil || second.ID != first.ID {
		t.Fatal("login not idempotent")
	}
	s := waitStatus(t, m, "demo", "pending")
	u, _ := url.Parse(s.URL)
	challenge = u.Query().Get("code_challenge")
	redirect = u.Query().Get("redirect_uri")
	if challenge == "" || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("missing PKCE")
	}
	resp, err := http.Get(redirect + "?state=wrong&code=test-code")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("bad state accepted")
	}
	if CredentialReady(stateRoot, "demo", upstream.URL+"/mcp") {
		t.Fatal("credentials saved before valid callback")
	}
	resp, err = http.Get(redirect + "?" + url.Values{"state": {u.Query().Get("state")}, "code": {"test-code"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitStatus(t, m, "demo", "authorized")
	path, _ := credentialPath(stateRoot, "demo")
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("credentials are not private")
	}
	status, _ := New(ctx, connector.Sources{ExternalRoot: root}, nil).Status(ctx, "demo")
	if status.Status != "authorized" {
		t.Fatal("login not restored after restart")
	}
	unlock, err := lockCredentials(ctx, stateRoot, "demo")
	if err != nil {
		t.Fatal(err)
	}
	cred, _ := readCredential(stateRoot, "demo")
	cred.Token.Expiry = time.Now().Add(-time.Hour)
	err = saveCredential(stateRoot, "demo", cred)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	transport := AuthorizingTransport{Base: http.DefaultTransport, Client: upstream.Client(), Root: stateRoot, ID: "demo", Resource: upstream.URL + "/mcp"}
	client := &http.Client{Transport: transport}
	resp, err = client.Get(upstream.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if refreshCount.Load() != 1 {
		t.Fatal("no refresh")
	}
	cred, _ = readCredential(stateRoot, "demo")
	if cred.Token.RefreshToken != "rotated-refresh" {
		t.Fatal("rotated token not persisted")
	}
	if _, err = client.Get(upstream.URL + "/other"); err == nil {
		t.Fatal("credential escaped resource")
	}
	data, _ := json.Marshal(status)
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "private-token") {
		t.Fatal("public status leaked credentials")
	}
	if err = m.Logout(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Get(upstream.URL + "/mcp"); err == nil {
		t.Fatal("old session still authorized after logout")
	}
}
