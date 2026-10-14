package connectorauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/connector"
)

func TestDiscoveryUsesInitializeChallengeOnlyWhenGETHasNone(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		getStatus                       int
		getChallenge, explicit, session bool
		wantGet, wantPost               int
	}{
		{name: "GET landing page", getStatus: 200, wantGet: 1, wantPost: 1},
		{name: "GET unsupported", getStatus: 405, wantGet: 1, wantPost: 1},
		{name: "GET challenge", getStatus: 401, getChallenge: true, wantGet: 1},
		{name: "explicit metadata", explicit: true},
		{name: "temporary session", getStatus: 200, session: true, wantGet: 1, wantPost: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var server *httptest.Server
			get, post, deleted := 0, 0, 0
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("discovery leaked authentication")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/mcp":
					switch r.Method {
					case http.MethodGet:
						get++
						if tc.getChallenge {
							w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+server.URL+`/gateway/metadata", scope="map:read"`)
						}
						w.WriteHeader(tc.getStatus)
					case http.MethodPost:
						post++
						var body struct {
							JSONRPC, Method string
							ID              int
							Params          struct {
								ProtocolVersion string
								Capabilities    map[string]any
								ClientInfo      map[string]string
							}
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.JSONRPC != "2.0" || body.ID != 1 || body.Method != "initialize" || body.Params.ProtocolVersion == "" || len(body.Params.Capabilities) != 0 || body.Params.ClientInfo["name"] != "agent-platform" {
							t.Errorf("unexpected probe: %+v %v", body, err)
						}
						if r.Header.Get("Content-Type") != "application/json" || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
							t.Error("missing MCP request headers")
						}
						w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+server.URL+`/gateway/metadata", scope="map:read"`)
						if tc.session {
							w.Header().Set("Mcp-Session-Id", "probe-session")
							w.WriteHeader(200)
						} else {
							w.WriteHeader(401)
						}
					case http.MethodDelete:
						deleted++
						if r.Header.Get("Mcp-Session-Id") != "probe-session" {
							t.Error("cleanup selected another session")
						}
						if r.Header.Get("MCP-Protocol-Version") != "" {
							t.Error("cleanup assumed a negotiated protocol version")
						}
						w.WriteHeader(204)
					default:
						t.Errorf("unexpected method %s", r.Method)
					}
				case "/gateway/metadata":
					json.NewEncoder(w).Encode(map[string]any{"resource": server.URL + "/mcp", "authorization_servers": []string{server.URL}})
				case "/.well-known/oauth-authorization-server":
					json.NewEncoder(w).Encode(map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "code_challenge_methods_supported": []string{"S256"}})
				default:
					t.Errorf("unexpected fallback %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			m := New(t.Context(), connector.Sources{ExternalRoot: t.TempDir()}, nil)
			settings := oauthSettings{ClientID: "test"}
			if tc.explicit {
				settings.ResourceMetadataURL = server.URL + "/gateway/metadata"
			}
			var scopes []string
			_, _, err := m.discover(t.Context(), server.URL+"/mcp", server.URL+"/mcp", settings, &scopes)
			if err != nil {
				t.Fatal(err)
			}
			if get != tc.wantGet || post != tc.wantPost {
				t.Fatalf("GET=%d POST=%d", get, post)
			}
			if !tc.explicit && (len(scopes) != 1 || scopes[0] != "map:read") {
				t.Fatalf("lost challenge scopes: %v", scopes)
			}
			if (deleted == 1) != tc.session {
				t.Fatalf("session cleanup count %d", deleted)
			}
		})
	}
}

func TestDiscoveryProbeDoesNotHideFailuresOrFollowRedirects(t *testing.T) {
	for _, tc := range []struct {
		name                string
		status              int
		challenge, location string
	}{
		{name: "server failure", status: 503},
		{name: "rate limit", status: 429},
		{name: "malformed challenge", status: 401, challenge: `Bearer resource_metadata="unterminated`},
		{name: "insecure metadata", status: 401, challenge: `Bearer resource_metadata="http://untrusted.invalid/metadata"`},
		{name: "redirect", status: 307, location: "/other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/mcp" {
					t.Error("followed redirect or default metadata after failure")
				}
				if r.Method == http.MethodGet {
					w.WriteHeader(200)
					return
				}
				w.Header().Set("WWW-Authenticate", tc.challenge)
				w.Header().Set("Location", tc.location)
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			m := New(t.Context(), connector.Sources{ExternalRoot: t.TempDir()}, nil)
			if _, _, err := m.discover(t.Context(), server.URL+"/mcp", server.URL+"/mcp", oauthSettings{ClientID: "test"}); err == nil {
				t.Fatal("failure was hidden")
			}
			if calls != 2 {
				t.Fatalf("unexpected requests: %d", calls)
			}
		})
	}
}

type discoveryProbeBody struct {
	io.Reader
	closed bool
}

func (b *discoveryProbeBody) Close() error { b.closed = true; return nil }
func TestDiscoveryProbeClosesBodiesAndCancelsRequests(t *testing.T) {
	m := New(t.Context(), connector.Sources{ExternalRoot: t.TempDir()}, nil)
	body := &discoveryProbeBody{Reader: strings.NewReader("landing page")}
	var requestContext context.Context
	calls := 0
	m.client = &http.Client{Transport: oauthTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			requestContext = r.Context()
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
		}
		if !body.closed || requestContext.Err() == nil {
			t.Error("GET body/context was left open")
		}
		return nil, fmt.Errorf("synthetic transport failure")
	})}
	if _, _, err := m.discover(t.Context(), "https://example.test/mcp", "https://example.test/mcp", oauthSettings{}); err == nil {
		t.Fatal("network failure hidden")
	}
	if calls != 2 {
		t.Fatalf("unexpected retries: %d", calls)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	m.client.Transport = oauthTestRoundTripper(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	start := time.Now()
	if _, _, err := m.discover(ctx, "https://example.test/mcp", "https://example.test/mcp", oauthSettings{}); err == nil || time.Since(start) > time.Second {
		t.Fatal("probe ignored cancellation", err)
	}
}
