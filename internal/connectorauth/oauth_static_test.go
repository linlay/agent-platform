package connectorauth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"agent-platform/internal/connector"
	"golang.org/x/oauth2"
)

func TestStandaloneOAuthGrantRequiresMCPSignIn(t *testing.T) {
	root := t.TempDir()
	resource := "https://example.test/mcp"
	// Grants saved by the removed standalone OAuth flow have mcp=false.
	legacy := oauthCredential{Resource: resource, Destination: resource, Token: &oauth2.Token{AccessToken: "legacy", RefreshToken: "legacy-refresh"}}
	if err := saveCredential(root, "demo", legacy); err != nil {
		t.Fatal(err)
	}
	if CredentialReady(root, "demo", resource, resource) {
		t.Fatal("standalone OAuth grant was reused for MCP authorization")
	}
	if _, err := AccessToken(t.Context(), root, "demo", resource, nil, resource); !errors.Is(err, ErrOAuthAuthorizationRequired) {
		t.Fatal("standalone OAuth grant must require sign-in", err)
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
	component := map[string]map[string]any{"main": {"type": "streamableHttp", "url": "https://example.test/mcp"}}
	for settings, want := range map[string]string{
		`{"authorizationEndpoint":"https://idp.test/authorize"}`: `unknown field "authorizationEndpoint"`,
		`{"discovery":true}`:                              `unknown field "discovery"`,
		`{"client_secret":"private"}`:                     `unknown field "client_secret"`,
		`{"resource":"http://example.test"}`:              `oauth.resource "http://example.test" must be an HTTPS URL`,
		`{"redirectUri":"https://example.test/callback"}`: "oauth.redirectUri must be an HTTP 127.0.0.1 URL",
	} {
		pkg := connector.Package{Manifest: connector.Manifest{ID: "demo", AuthMode: connector.AuthMCP, OAuth: json.RawMessage(settings)}, MCP: component}
		if err := ValidatePackage(pkg); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: error %v, want %q", settings, err, want)
		}
	}
	cliOnly := connector.Package{Manifest: connector.Manifest{ID: "demo", AuthMode: connector.AuthMCP}}
	if _, _, err := oauthResource(cliOnly); err == nil || !strings.Contains(err.Error(), "CLI-only and standalone OAuth are not supported") {
		t.Fatal("CLI-only MCP authorization accepted", err)
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
	if err := saveCredential(root, "demo", oauthCredential{MCP: true, Resource: resource, Destination: destination, Token: &oauth2.Token{AccessToken: "test-token"}}); err != nil {
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
