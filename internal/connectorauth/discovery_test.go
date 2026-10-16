package connectorauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/connector"
)

// mcpAuthUpstream is an MCP server and authorization server on one host. Like
// Tencent Map's gateway, GET on the MCP endpoint answers 200 without a
// challenge; only an unauthenticated initialize POST names the metadata.
type mcpAuthUpstream struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
	headers  []http.Header
	// Overrides; nil keeps the compliant default.
	mcp, metadata, authServer, token http.HandlerFunc
	routes                           map[string]http.HandlerFunc
}

func newMCPAuthUpstream(t *testing.T, configure func(*mcpAuthUpstream)) *mcpAuthUpstream {
	t.Helper()
	u := &mcpAuthUpstream{}
	if configure != nil {
		configure(u)
	}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.requests = append(u.requests, r.Method+" "+r.URL.Path)
		u.headers = append(u.headers, r.Header.Clone())
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if route := u.routes[r.URL.Path]; route != nil {
			route(w, r)
			return
		}
		switch r.URL.Path {
		case "/mcp":
			if u.mcp != nil {
				u.mcp(w, r)
				return
			}
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
				w.Write([]byte(`{}`))
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+u.URL+`/gateway/resource-metadata", scope="mcp:read"`)
			w.WriteHeader(http.StatusUnauthorized)
		case "/gateway/resource-metadata":
			if u.metadata != nil {
				u.metadata(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"resource": u.URL + "/mcp", "authorization_servers": []string{u.URL}, "scopes_supported": []string{"mcp:read", "mcp:write"}})
		case "/.well-known/oauth-authorization-server":
			if u.authServer != nil {
				u.authServer(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"issuer": u.URL, "authorization_endpoint": u.URL + "/authorize", "token_endpoint": u.URL + "/token",
				"registration_endpoint": u.URL + "/register", "response_types_supported": []string{"code"},
				"code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"},
			})
		case "/register":
			var registration map[string]any
			json.NewDecoder(r.Body).Decode(&registration)
			registration["client_id"] = "test-client"
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(registration)
		case "/token":
			if u.token != nil {
				u.token(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh", "token_type": "Bearer", "expires_in": 3600})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *mcpAuthUpstream) requestLog() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.requests...)
}

func (u *mcpAuthUpstream) writePackage(t *testing.T, root string, component map[string]any) connector.Package {
	t.Helper()
	if component == nil {
		component = map[string]any{}
	}
	component["type"], component["url"] = "streamableHttp", u.URL+"/mcp"
	return writeAuthPackage(t, root, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": "mcp"}, component)
}

func TestDiscoveryUsesInitializeChallengeWhenGETReturnsOK(t *testing.T) {
	upstream := newMCPAuthUpstream(t, nil)
	pkg := upstream.writePackage(t, t.TempDir(), map[string]any{"headers": map[string]any{"McpGw-Tools-ID": "500038", "X-Api-Key": "${API_KEY}"}})
	report := DiscoverAuthorization(t.Context(), upstream.Client(), pkg, "")
	if report.Status != "passed" || report.ResourceMetadataURL != upstream.URL+"/gateway/resource-metadata" || report.Issuer != upstream.URL || report.ClientRegistration != "dynamic" {
		t.Fatalf("discovery report = %+v", report)
	}
	if len(report.Scopes) != 1 || report.Scopes[0] != "mcp:read" {
		t.Fatalf("challenge scopes not preferred: %v", report.Scopes)
	}
	if log := upstream.requestLog(); log[0] != "POST /mcp" || strings.Contains(strings.Join(log, ","), "GET /mcp") {
		t.Fatalf("discovery must probe with initialize POST only: %v", log)
	}
	probe := upstream.headers[0]
	if probe.Get("McpGw-Tools-ID") != "500038" || probe.Get("X-Api-Key") != "" || probe.Get("Authorization") != "" {
		t.Fatalf("probe headers = %v", probe)
	}
	if report.Steps[0].Result != "Bearer challenge with resource_metadata" || report.Steps[0].Status != http.StatusUnauthorized {
		t.Fatalf("probe step = %+v", report.Steps[0])
	}
}

func TestDiscoveryFallsBackToWellKnownMetadata(t *testing.T) {
	var upstream *mcpAuthUpstream
	upstream = newMCPAuthUpstream(t, func(u *mcpAuthUpstream) {
		u.mcp = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }
		// Root well-known metadata after the path-inserted location returns 404.
		u.routes = map[string]http.HandlerFunc{"/.well-known/oauth-protected-resource": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"resource": upstream.URL + "/mcp", "authorization_servers": []string{upstream.URL}})
		}}
	})
	pkg := upstream.writePackage(t, t.TempDir(), nil)
	report := DiscoverAuthorization(t.Context(), upstream.Client(), pkg, "")
	if report.Status != "passed" || report.ResourceMetadataURL != upstream.URL+"/.well-known/oauth-protected-resource" {
		t.Fatalf("fallback report = %+v", report)
	}
	if got := []string{report.Steps[0].Result, report.Steps[1].URL}; got[0] != "no Bearer challenge" || got[1] != upstream.URL+"/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("steps = %+v", report.Steps)
	}
}

func TestDiscoveryFailuresNameTheCauseAndRequests(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*mcpAuthUpstream)
		code      string
		message   string
	}{
		{"no metadata", func(u *mcpAuthUpstream) {
			u.mcp = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }
		}, "resource_metadata_not_found", "returned no Bearer challenge with resource_metadata and no well-known protected resource metadata is available"},
		{"resource mismatch", func(u *mcpAuthUpstream) {
			u.metadata = func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"resource": "https://other.test/mcp", "authorization_servers": []string{"https://other.test"}})
			}
		}, "invalid_resource_metadata", "is invalid for resource"},
		{"no registration", func(u *mcpAuthUpstream) {
			u.authServer = func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"issuer": "ISSUER", "authorization_endpoint": "https://idp.test/authorize", "token_endpoint": "https://idp.test/token", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}})
			}
		}, "", ""},
		{"no pkce", func(u *mcpAuthUpstream) {
			u.authServer = func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"issuer": "ISSUER", "authorization_endpoint": "https://idp.test/authorize", "token_endpoint": "https://idp.test/token", "registration_endpoint": "https://idp.test/register", "response_types_supported": []string{"code"}})
			}
		}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var upstream *mcpAuthUpstream
			upstream = newMCPAuthUpstream(t, func(u *mcpAuthUpstream) {
				tc.configure(u)
				if inner := u.authServer; inner != nil {
					// The issuer must equal the server URL, known only after start.
					u.authServer = func(w http.ResponseWriter, r *http.Request) {
						recorder := httptest.NewRecorder()
						inner(recorder, r)
						w.Write([]byte(strings.ReplaceAll(recorder.Body.String(), "ISSUER", upstream.URL)))
					}
				}
			})
			code, message := tc.code, tc.message
			if tc.name == "no registration" {
				code, message = "client_registration_unavailable", "supports neither dynamic client registration"
			}
			if tc.name == "no pkce" {
				code, message = "pkce_s256_unsupported", "does not advertise PKCE S256"
			}
			root := t.TempDir()
			pkg := upstream.writePackage(t, root, nil)
			report := DiscoverAuthorization(t.Context(), upstream.Client(), pkg, "")
			if report.Status != "failed" || report.Code != code || !strings.Contains(report.Message, message) || len(report.Steps) == 0 {
				t.Fatalf("report = %+v", report)
			}
			// Login reports the same cause together with every request made.
			m := New(t.Context(), connector.Sources{ExternalRoot: root}, nil)
			m.client = upstream.Client()
			if _, err := m.Start("demo"); err != nil {
				t.Fatal(err)
			}
			failed := waitStatus(t, m, "demo", "failed")
			if !strings.Contains(failed.Message, message) || !strings.Contains(failed.Message, "requests: POST "+upstream.URL+"/mcp -> ") {
				t.Fatalf("login failure message = %q", failed.Message)
			}
		})
	}
}

func TestDiscoveryClosesSessionOpenedByProbe(t *testing.T) {
	var deleted sync.WaitGroup
	deleted.Add(1)
	upstream := newMCPAuthUpstream(t, func(u *mcpAuthUpstream) {
		u.mcp = func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost:
				w.Header().Set("Mcp-Session-Id", "probe-session")
				w.Write([]byte(`{}`))
			case http.MethodDelete:
				if r.Header.Get("Mcp-Session-Id") == "probe-session" {
					deleted.Done()
				}
			}
		}
	})
	pkg := upstream.writePackage(t, t.TempDir(), nil)
	report := DiscoverAuthorization(t.Context(), upstream.Client(), pkg, "")
	deleted.Wait()
	if report.Code != "resource_metadata_not_found" {
		t.Fatalf("report = %+v", report)
	}
}

func TestCheckRunsDiscoveryForUnauthorizedMCP(t *testing.T) {
	upstream := newMCPAuthUpstream(t, nil)
	root := t.TempDir()
	upstream.writePackage(t, root, nil)
	m := New(t.Context(), connector.Sources{ExternalRoot: root}, nil)
	m.client = upstream.Client()
	session, err := m.Check(t.Context(), "demo", "")
	if err != nil || session.Status != "unauthorized" || session.Discovery == nil || session.Discovery.Status != "passed" || session.Message != "MCP authorization discovery passed; sign in to authorize" {
		t.Fatalf("check = %+v (%v)", session, err)
	}
	if strings.Contains(strings.Join(upstream.requestLog(), ","), "/register") {
		t.Fatal("check registered a client")
	}
}

func TestDiscoveryDoesNotWaitForStreamingProbeResponse(t *testing.T) {
	deleted := make(chan struct{}, 1)
	var upstream *mcpAuthUpstream
	upstream = newMCPAuthUpstream(t, func(u *mcpAuthUpstream) {
		u.mcp = func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				deleted <- struct{}{}
				return
			}
			// An SSE stream that stays open after the headers.
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Mcp-Session-Id", "stream-session")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(20 * time.Second):
			}
		}
		u.routes = map[string]http.HandlerFunc{"/.well-known/oauth-protected-resource": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"resource": upstream.URL + "/mcp", "authorization_servers": []string{upstream.URL}})
		}}
	})
	pkg := upstream.writePackage(t, t.TempDir(), nil)
	started := time.Now()
	report := DiscoverAuthorization(t.Context(), upstream.Client(), pkg, "")
	if report.Status != "passed" || time.Since(started) > 5*time.Second {
		t.Fatalf("streaming probe blocked discovery for %s: %+v", time.Since(started), report)
	}
	select {
	case <-deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("probe session was not closed")
	}
}

func TestCheckRediscoversAfterFailedLogin(t *testing.T) {
	var broken atomic.Bool
	broken.Store(true)
	var upstream *mcpAuthUpstream
	upstream = newMCPAuthUpstream(t, func(u *mcpAuthUpstream) {
		u.metadata = func(w http.ResponseWriter, r *http.Request) {
			if broken.Load() {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"resource": upstream.URL + "/mcp", "authorization_servers": []string{upstream.URL}})
		}
	})
	root := t.TempDir()
	upstream.writePackage(t, root, nil)
	m := New(t.Context(), connector.Sources{ExternalRoot: root}, nil)
	m.client = upstream.Client()
	if _, err := m.Start("demo"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "demo", "failed")
	broken.Store(false)
	before := len(upstream.requestLog())
	session, err := m.Check(t.Context(), "demo", "")
	if err != nil || session.Discovery == nil || session.Discovery.Status != "passed" || len(upstream.requestLog()) == before {
		t.Fatalf("check reused the failed login: %+v (%v)", session, err)
	}
	if status, _ := m.StatusComponent(t.Context(), "demo", ""); status.Status != "unauthorized" {
		t.Fatalf("check did not supersede the failed login: %+v", status)
	}
}

func TestDiscoveryDiagnosticsRedactURLQueries(t *testing.T) {
	const secret = "REVIEW_SECRET"
	upstream := newMCPAuthUpstream(t, func(u *mcpAuthUpstream) {
		u.mcp = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }
	})
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	for name, endpoint := range map[string]string{"no metadata": upstream.URL + "/mcp?api_key=" + secret, "unreachable": closed.URL + "/mcp?api_key=" + secret} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeAuthPackage(t, root, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": "mcp"}, map[string]any{"type": "streamableHttp", "url": endpoint})
			pkg, err := connector.Load(root, "demo")
			if err != nil {
				t.Fatal(err)
			}
			report := DiscoverAuthorization(t.Context(), upstream.Client(), pkg, "")
			encoded, _ := json.Marshal(report)
			if report.Status != "failed" || strings.Contains(string(encoded), secret) || !strings.Contains(report.Message, "?[redacted]") {
				t.Fatalf("diagnostics leaked the query: %s", encoded)
			}
			m := New(t.Context(), connector.Sources{ExternalRoot: root}, nil)
			m.client = upstream.Client()
			if _, err := m.Start("demo"); err != nil {
				t.Fatal(err)
			}
			if failed := waitStatus(t, m, "demo", "failed"); strings.Contains(failed.Message, secret) {
				t.Fatalf("login message leaked the query: %s", failed.Message)
			}
		})
	}
	pkg := connector.Package{Manifest: connector.Manifest{ID: "demo", AuthMode: connector.AuthMCP}, MCP: map[string]map[string]any{"main": {"type": "streamableHttp", "url": "http://example.test/mcp?api_key=" + secret}}}
	if err := ValidatePackage(pkg); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("local validation leaked the query: %v", err)
	}
}

func TestDiscoveryReportsTheScopesLoginRequests(t *testing.T) {
	upstream := newMCPAuthUpstream(t, nil)
	root := t.TempDir()
	writeAuthPackage(t, root, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": "mcp", "oauth": map[string]any{"scopes": []string{"configured:scope"}}}, map[string]any{"type": "streamableHttp", "url": upstream.URL + "/mcp"})
	pkg, err := connector.Load(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	report := DiscoverAuthorization(t.Context(), upstream.Client(), pkg, "")
	m := New(t.Context(), connector.Sources{ExternalRoot: root}, nil)
	m.client = upstream.Client()
	if _, err := m.Start("demo"); err != nil {
		t.Fatal(err)
	}
	session := waitStatus(t, m, "demo", "pending")
	// End the login before the temporary state directory is removed.
	if err := m.Cancel("demo"); err != nil {
		t.Fatal(err)
	}
	authorize, _ := url.Parse(session.URL)
	if strings.Join(report.Scopes, " ") != "mcp:read" || authorize.Query().Get("scope") != "mcp:read" {
		t.Fatalf("check scopes %v, login scope %q", report.Scopes, authorize.Query().Get("scope"))
	}
}

func TestDiagnosticRedactionHandlesSpecialCharacters(t *testing.T) {
	const secret = "REVIEW_SECRET"
	for _, text := range []string{
		`Post "https://h.test/mcp?api_key='` + secret + `'": dial tcp: refused`,
		`url "https://h.test/mcp?api_key=\"` + secret + `\"" must be HTTPS`,
		`metadata https://h.test/mcp?k=<` + secret + `> failed`,
		`endpoint https://user:` + secret + `@h.test/mcp failed`,
		`endpoint https://h.test/mcp#` + secret + ` failed`,
	} {
		if got := redactDiagnostic(text); strings.Contains(got, secret) {
			t.Fatalf("redactDiagnostic(%q) = %q", text, got)
		}
	}
	for _, raw := range []string{"https://h.test/mcp?api_key='" + secret + "'", "https://h.test/mcp?a=\"" + secret + "\"", "https://h.test/mcp#" + secret, "https://h.test/%zz?" + secret, "https://user:" + secret + "@h.test/%zz?key=x"} {
		if got := redactURLString(raw); strings.Contains(got, secret) {
			t.Fatalf("redactURLString(%q) = %q", raw, got)
		}
	}
}

func TestDiscoveryRedactsQueriesWithSpecialCharacters(t *testing.T) {
	const secret = "REVIEW_SECRET"
	upstream := newMCPAuthUpstream(t, func(u *mcpAuthUpstream) {
		u.mcp = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }
	})
	for _, suffix := range []string{"?api_key='" + secret + "'", `?api_key="` + secret + `"`, "?api_key=<" + secret + ">", "?a=1&api_key=" + secret + `'x"y`, "#" + secret} {
		t.Run(suffix, func(t *testing.T) {
			root := t.TempDir()
			writeAuthPackage(t, root, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": "mcp"}, map[string]any{"type": "streamableHttp", "url": upstream.URL + "/mcp" + suffix})
			pkg, err := connector.Load(root, "demo")
			if err != nil {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("load error leaked the secret: %v", err)
				}
				return
			}
			report := DiscoverAuthorization(t.Context(), upstream.Client(), pkg, "")
			if encoded, _ := json.Marshal(report); report.Status != "failed" || strings.Contains(string(encoded), secret) {
				t.Fatalf("report leaked the secret: %s", encoded)
			}
			m := New(t.Context(), connector.Sources{ExternalRoot: root}, nil)
			m.client = upstream.Client()
			if _, err := m.Start("demo"); err != nil {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("start error leaked the secret: %v", err)
				}
				return
			}
			if failed := waitStatus(t, m, "demo", "failed"); strings.Contains(failed.Message, secret) {
				t.Fatalf("login message leaked the secret: %s", failed.Message)
			}
		})
	}
}

func TestEarlierCheckKeepsNewerLoginFailure(t *testing.T) {
	release := make(chan struct{})
	var probes atomic.Int32
	upstream := newMCPAuthUpstream(t, func(u *mcpAuthUpstream) {
		u.mcp = func(w http.ResponseWriter, r *http.Request) {
			// The first probe belongs to check A and waits; login B proceeds.
			if probes.Add(1) == 1 {
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	root := t.TempDir()
	upstream.writePackage(t, root, nil)
	m := New(t.Context(), connector.Sources{ExternalRoot: root}, nil)
	m.client = upstream.Client()
	checked := make(chan Session, 1)
	go func() {
		session, _ := m.Check(t.Context(), "demo", "")
		checked <- session
	}()
	for probes.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := m.Start("demo"); err != nil {
		t.Fatal(err)
	}
	failure := waitStatus(t, m, "demo", "failed")
	close(release)
	if session := <-checked; session.Discovery == nil {
		t.Fatalf("check A did not run discovery: %+v", session)
	}
	status, err := m.StatusComponent(t.Context(), "demo", "")
	if err != nil || status.Status != "failed" || status.Message != failure.Message {
		t.Fatalf("earlier check removed the newer login failure: %+v (%v)", status, err)
	}
}

func TestValidationRedactsUnparseableURLCredentials(t *testing.T) {
	const secret = "REVIEW_SECRET"
	pkg := connector.Package{Manifest: connector.Manifest{ID: "demo", AuthMode: connector.AuthMCP}, MCP: map[string]map[string]any{"main": {"type": "streamableHttp", "url": "https://user:" + secret + "@h.test/%zz?key=x"}}}
	if err := ValidatePackage(pkg); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("validation leaked userinfo: %v", err)
	}
}
