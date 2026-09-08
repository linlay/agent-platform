package connectorauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"golang.org/x/oauth2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"agent-platform/internal/connector"
)

func TestGenericOAuthUsesConfiguredClientAndEndpoints(t *testing.T) {
	challenge := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			t.Errorf("ordinary OAuth performed discovery/registration: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != <-challenge || r.Form.Get("resource") != "" || r.Form.Get("client_id") != "public-client" || r.Form.Get("code") != "code" {
			t.Error("incorrect static OAuth exchange")
			http.Error(w, "invalid", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "private-token", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer upstream.Close()
	sources := connector.Sources{ExternalRoot: t.TempDir()}
	pkg := writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{
		"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "oauth",
		"oauth": map[string]any{"client_id": "public-client", "authorization_endpoint": upstream.URL + "/authorize", "token_endpoint": upstream.URL + "/token", "resource": upstream.URL + "/mcp", "scopes": []string{"read"}},
	}, map[string]any{"type": "streamableHttp", "url": upstream.URL + "/mcp"})
	if err := ValidatePackage(pkg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, sources, nil)
	if _, err := m.Start("demo"); err != nil {
		t.Fatal(err)
	}
	session := waitStatus(t, m, "demo", "pending")
	u, err := url.Parse(session.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/authorize" || u.Query().Get("client_id") != "public-client" || u.Query().Get("resource") != "" || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("incorrect authorization URL")
	}
	challenge <- u.Query().Get("code_challenge")
	response, err := http.Get(u.Query().Get("redirect_uri") + "?" + url.Values{"state": {u.Query().Get("state")}, "code": {"code"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	waitStatus(t, m, "demo", "authorized")
	if !CredentialReady(sources.PersistentRoot(), "demo", upstream.URL+"/mcp") {
		t.Fatal("credential not bound to resource")
	}
	if err := m.Logout(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthModeValidationAndCallbacks(t *testing.T) {
	for _, raw := range []string{"https://example.test/callback", "http://localhost:80/callback", "http://127.0.0.1/callback", "http://127.0.0.1:0/callback", "http://127.0.0.1:8080/callback?x=y"} {
		if _, _, err := callbackAddress(raw); err == nil {
			t.Fatal("unsafe callback", raw)
		}
	}
	for _, raw := range []string{"", "http://127.0.0.1:8080/callback"} {
		if _, _, err := callbackAddress(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, settings := range []string{
		`{"client_id":"public"}`, `{"discovery":true}`, `{"client_secret":"private"}`,
	} {
		pkg := connector.Package{Manifest: connector.Manifest{AuthMode: connector.AuthOAuth, OAuth: json.RawMessage(settings)}}
		if err := ValidatePackage(pkg); err == nil {
			t.Fatal("invalid OAuth accepted")
		}
	}
}

func TestMCPOAuthResourceCanDifferFromDestination(t *testing.T) {
	root := t.TempDir()
	resource := "https://mail.example.test"
	destination := resource + "/mcp"
	pkg := connector.Package{Manifest: connector.Manifest{ID: "demo", AuthMode: connector.AuthMCP, OAuth: json.RawMessage(`{"resource":"https://mail.example.test"}`)}, MCP: map[string]map[string]any{"main": {"type": "streamableHttp", "url": destination}}}
	if actual, err := OAuthResource(pkg); err != nil || actual != resource {
		t.Fatal("resource differs", actual, err)
	}
	if err := saveCredential(root, "demo", oauthCredential{Resource: resource, Destination: destination, Token: &oauth2.Token{AccessToken: "test-token"}}); err != nil {
		t.Fatal(err)
	}
	if !CredentialReady(root, "demo", resource, destination) || CredentialReady(root, "demo", resource, resource+"/other") {
		t.Fatal("destination binding lost")
	}
	received := 0
	transport := AuthorizingTransport{Base: oauthTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		received++
		if req.URL.String() != destination || req.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("wrong credential request")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}), Root: root, ID: "demo", Resource: destination, CredentialResource: resource}
	request, _ := http.NewRequest(http.MethodPost, destination, nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	request, _ = http.NewRequest(http.MethodPost, resource+"/other", nil)
	if _, err := transport.RoundTrip(request); err == nil || received != 1 {
		t.Fatal("credential escaped endpoint")
	}
}

type oauthTestRoundTripper func(*http.Request) (*http.Response, error)

func (f oauthTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
