package connectorauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
	files := map[string]any{"connector.json": manifest, "mcp.json": map[string]any{"mcpServers": map[string]any{"main": component}}}
	// Packages keep the credential form in token-schema.json, not the manifest.
	if schema, ok := manifest["tokenSchema"]; ok {
		manifest = maps.Clone(manifest)
		delete(manifest, "tokenSchema")
		files["connector.json"], files["token-schema.json"] = manifest, schema
	}
	for name, value := range files {
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

func newOAuthRegistrationManager(t *testing.T, supportedGrants []string, register http.HandlerFunc) *Manager {
	t.Helper()
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mcp":
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+upstream.URL+`/resource-metadata", scope="context:read"`)
			w.WriteHeader(http.StatusUnauthorized)
		case "/resource-metadata":
			json.NewEncoder(w).Encode(map[string]any{"resource": upstream.URL + "/mcp", "authorization_servers": []string{upstream.URL}})
		case "/.well-known/oauth-authorization-server":
			metadata := map[string]any{
				"issuer": upstream.URL, "authorization_endpoint": upstream.URL + "/authorize",
				"token_endpoint": upstream.URL + "/token", "registration_endpoint": upstream.URL + "/register",
				"response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"},
				"token_endpoint_auth_methods_supported": []string{"none"},
			}
			if supportedGrants != nil {
				metadata["grant_types_supported"] = supportedGrants
			}
			json.NewEncoder(w).Encode(metadata)
		case "/register":
			register(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	sources := connector.Sources{ExternalRoot: t.TempDir()}
	writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{
		"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": "mcp",
	}, map[string]any{"type": "streamableHttp", "url": upstream.URL + "/mcp"})
	m := New(t.Context(), sources, nil)
	t.Cleanup(func() {
		if err := m.Cancel("demo"); err != nil {
			t.Error(err)
		}
	})
	return m
}

func TestMCPOAuthRegistrationUsesAdvertisedGrantTypes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		supported []string
		want      []string
	}{
		{"authorization_code_only", []string{"authorization_code"}, []string{"authorization_code"}},
		{"refresh_supported", []string{"authorization_code", "refresh_token"}, []string{"authorization_code", "refresh_token"}},
		{"omitted_preserves_refresh", nil, []string{"authorization_code", "refresh_token"}},
		{"explicit_empty", []string{}, []string{"authorization_code"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newOAuthRegistrationManager(t, tc.supported, func(w http.ResponseWriter, r *http.Request) {
				var registration struct {
					GrantTypes []string `json:"grant_types"`
				}
				if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if !slices.Equal(registration.GrantTypes, tc.want) {
					t.Errorf("registration grant_types = %v, want %v", registration.GrantTypes, tc.want)
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client_metadata"})
					return
				}
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]string{"client_id": "test-client", "token_endpoint_auth_method": "none"})
			})
			if _, err := m.Start("demo"); err != nil {
				t.Fatal(err)
			}
			session := waitStatus(t, m, "demo", "pending")
			u, err := url.Parse(session.URL)
			if err != nil {
				t.Fatal(err)
			}
			if u.Path != "/authorize" || u.Query().Get("client_id") != "test-client" || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("resource") == "" || u.Query().Get("scope") != "context:read" {
				t.Fatalf("incorrect authorization URL: %s", session.URL)
			}
		})
	}
}

func TestMCPOAuthRegistrationFailureMessage(t *testing.T) {
	const generic = "OAuth dynamic client registration failed"
	longError, _ := json.Marshal(map[string]string{
		"error": strings.Repeat("x", 2000), "error_description": strings.Repeat("说明", 2000) + "truncated-tail",
	})
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"error_code", http.StatusBadRequest, `{"error":"invalid_client_metadata"}`, generic + ": invalid_client_metadata"},
		{"description", http.StatusBadRequest, `{"error":"invalid_client_metadata","error_description":"Unsupported grant type: refresh_token"}`, generic + ": invalid_client_metadata: Unsupported grant type: refresh_token"},
		{"bounded_details", http.StatusBadRequest, string(longError), ""},
		{"empty_details", http.StatusBadRequest, `{}`, generic},
		{"malformed_error", http.StatusBadRequest, `private-response-body`, generic},
		{"server_error", http.StatusInternalServerError, `{"error":"invalid_client_metadata","error_description":"private-response-body"}`, generic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newOAuthRegistrationManager(t, []string{"authorization_code"}, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			})
			if _, err := m.Start("demo"); err != nil {
				t.Fatal(err)
			}
			session := waitStatus(t, m, "demo", "failed")
			if tc.want != "" && session.Message != tc.want {
				t.Fatalf("failure message = %q, want %q", session.Message, tc.want)
			}
			if tc.name == "bounded_details" {
				if !strings.HasPrefix(session.Message, generic+": x") || !strings.Contains(session.Message, "说明") || strings.Contains(session.Message, "truncated-tail") || len([]rune(session.Message)) > 700 {
					t.Fatalf("registration error details were lost or not bounded: %q", session.Message)
				}
			}
			if session.URL != "" {
				t.Fatal("failed registration exposed an authorization URL")
			}
		})
	}
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
	writeAuthPackage(t, root, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": "mcp", "oauth": map[string]any{"resourceMetadataUrl": upstream.URL + "/resource-metadata"}}, map[string]any{"type": "streamableHttp", "url": upstream.URL + "/mcp"})
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
