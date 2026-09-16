package connectorauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/connector"
	"golang.org/x/oauth2"
)

func TestMultiResourceOAuthAuthorizesEveryComponentAndRevokesEveryToken(t *testing.T) {
	var upstream *httptest.Server
	var mu sync.Mutex
	revoked := map[string]bool{}
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/one", "/two":
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+upstream.URL+"/metadata"+r.URL.Path+`"`)
			w.WriteHeader(401)
		case "/metadata/one", "/metadata/two":
			json.NewEncoder(w).Encode(map[string]any{"resource": upstream.URL + strings.TrimPrefix(r.URL.Path, "/metadata"), "authorization_servers": []string{upstream.URL}})
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]any{"issuer": upstream.URL, "authorization_endpoint": upstream.URL + "/authorize", "token_endpoint": upstream.URL + "/token", "registration_endpoint": upstream.URL + "/register", "revocation_endpoint": upstream.URL + "/revoke", "revocation_endpoint_auth_methods_supported": []string{"none"}, "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}})
		case "/register":
			var value map[string]any
			json.NewDecoder(r.Body).Decode(&value)
			value["client_id"] = "multi-client"
			json.NewEncoder(w).Encode(value)
		case "/token":
			r.ParseForm()
			resource := r.Form.Get("resource")
			if resource != upstream.URL+"/one" && resource != upstream.URL+"/two" {
				t.Error("wrong authorization resource")
				http.Error(w, "bad resource", 400)
				return
			}
			suffix := strings.TrimPrefix(resource, upstream.URL+"/")
			if r.Form.Get("code_verifier") == "" || r.Form.Get("code") != suffix {
				t.Error("missing PKCE or wrong code")
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access-" + suffix, "refresh_token": "refresh-" + suffix, "token_type": "Bearer", "expires_in": 3600})
		case "/revoke":
			r.ParseForm()
			if r.Form.Get("client_id") != "multi-client" {
				t.Error("missing revocation client")
			}
			mu.Lock()
			revoked[r.Form.Get("token")] = true
			mu.Unlock()
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	sources := connector.Sources{ExternalRoot: t.TempDir(), Owner: "user:alice"}
	dir := filepath.Join(sources.ExternalRoot, "multi")
	os.MkdirAll(dir, 0700)
	for name, value := range map[string]any{
		"connector.json": map[string]any{"id": "multi", "name": "Multi", "version": "1.0.0", "type": "mcp", "auth_mode": "mcp"},
		"mcp.json":       map[string]any{"mcpServers": map[string]any{"a": map[string]any{"type": "streamableHttp", "url": upstream.URL + "/one"}, "b": map[string]any{"type": "streamableHttp", "url": upstream.URL + "/two"}}},
	} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := sources.Load("multi")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePackage(pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := OAuthResource(pkg); err == nil {
		t.Fatal("ambiguous global OAuth resource accepted")
	}
	if got, err := OAuthResourceForComponent(pkg, "b"); err != nil || got != upstream.URL+"/two" {
		t.Fatal(got, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	m := New(ctx, sources, nil)
	if _, err := m.Start(pkg.ID); err != nil {
		t.Fatal(err)
	}
	for index, suffix := range []string{"one", "two"} {
		var authorize *url.URL
		for authorize == nil {
			m.mu.Lock()
			session := m.sessions[pkg.ID].Session
			m.mu.Unlock()
			if session.Status == "failed" {
				t.Fatal(session.Message)
			}
			parsed, _ := url.Parse(session.URL)
			if parsed != nil && parsed.Query().Get("resource") == upstream.URL+"/"+suffix {
				authorize = parsed
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("authorization URL not exposed", suffix)
			case <-time.After(10 * time.Millisecond):
			}
		}
		if index == 1 {
			status, err := New(ctx, sources, nil).Status(ctx, pkg.ID)
			if err != nil || status.Status == "authorized" {
				t.Fatalf("partial resource login reported complete: %v %v", status, err)
			}
		}
		callback := authorize.Query().Get("redirect_uri") + "?" + url.Values{"state": {authorize.Query().Get("state")}, "code": {suffix}}.Encode()
		response, err := http.Get(callback)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	waitStatus(t, m, pkg.ID, "authorized")
	fresh, err := New(ctx, sources, nil).Status(ctx, pkg.ID)
	if err != nil || fresh.Status != "authorized" {
		t.Fatal(fresh, err)
	}
	for _, suffix := range []string{"one", "two"} {
		resource := upstream.URL + "/" + suffix
		token, err := AccessToken(ctx, pkg.CredentialRoot(), pkg.ID, resource, upstream.Client(), resource)
		if err != nil || token != "access-"+suffix {
			t.Fatalf("wrong %s token: %q %v", suffix, token, err)
		}
	}
	if _, err := AccessToken(ctx, pkg.CredentialRoot(), pkg.ID, upstream.URL+"/one", upstream.Client(), upstream.URL+"/two"); err == nil {
		t.Fatal("cross-resource token reused")
	}
	state, _ := pkg.UserStateDir()
	stored, err := readCredential(pkg.CredentialRoot(), pkg.ID)
	if err != nil || len(stored.Grants) != 1 {
		t.Fatal("independent credential grants missing", err)
	}
	if err := m.Logout(ctx, pkg.ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"access-one", "refresh-one", "access-two", "refresh-two"} {
		if !revoked[name] {
			t.Fatalf("token %s was not revoked", name)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "oauth.json")); !os.IsNotExist(err) {
		t.Fatal("resource credentials retained after logout")
	}
}

func TestOAuthResourceIndexRefreshDoesNotOverwriteOtherResource(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("refresh_token") != "refresh-one" {
			t.Error("wrong resource refresh token")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "new-one", "refresh_token": "rotated-one", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer upstream.Close()
	root := t.TempDir()
	for _, name := range []string{"one", "two"} {
		expiry := time.Now().Add(time.Hour)
		if name == "one" {
			expiry = time.Now().Add(-time.Hour)
		}
		c := oauthCredential{Resource: "https://example.test/" + name, Destination: "https://example.test/" + name, Config: oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{TokenURL: upstream.URL, AuthStyle: oauth2.AuthStyleInParams}}, Token: &oauth2.Token{AccessToken: "old-" + name, RefreshToken: "refresh-" + name, Expiry: expiry}}
		if err := saveResourceCredential(root, "demo", c); err != nil {
			t.Fatal(err)
		}
	}
	token, err := AccessToken(t.Context(), root, "demo", "https://example.test/one", upstream.Client())
	if err != nil || token != "new-one" {
		t.Fatal(token, err)
	}
	token, err = AccessToken(t.Context(), root, "demo", "https://example.test/two", upstream.Client())
	if err != nil || token != "old-two" {
		t.Fatal(token, err)
	}
	first, _, err := readResourceCredential(root, "demo", "https://example.test/one", nil)
	if err != nil || first.Token.RefreshToken != "rotated-one" {
		t.Fatal("rotated first token not persisted", err)
	}
}

func TestOAuthResourceIndexDoesNotFallbackOnTamperedOrDifferentBindings(t *testing.T) {
	root := t.TempDir()
	resource := "https://example.test/a"
	if err := saveCredential(root, "demo", oauthCredential{Resource: resource, Token: &oauth2.Token{AccessToken: "legacy"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := AccessToken(t.Context(), root, "demo", "https://example.test/b", nil); err == nil {
		t.Fatal("different resource borrowed legacy token")
	}
	if err := saveResourceCredential(root, "demo", oauthCredential{Resource: resource, Token: &oauth2.Token{AccessToken: "new"}}); err != nil {
		t.Fatal(err)
	}
	path, _ := credentialPath(root, "demo")
	if err := os.WriteFile(path, []byte(`{"resource":"https://evil.test","token":{"access_token":"wrong"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := AccessToken(t.Context(), root, "demo", resource, nil); err == nil {
		t.Fatal("tampered index silently used legacy token")
	}
}

func TestOAuthDisconnectClearsPrivateTokensWhenRemoteRevocationFailsOrUnsupported(t *testing.T) {
	for _, scenario := range []string{"success", "failure", "unsupported", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			leaked := false
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true; w.WriteHeader(200) }))
			defer other.Close()
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "failure" {
					http.Error(w, "rejected", 503)
					return
				}
				if scenario == "redirect" {
					http.Redirect(w, r, other.URL, 307)
					return
				}
				w.WriteHeader(200)
			}))
			defer endpoint.Close()
			sources := connector.Sources{ExternalRoot: t.TempDir(), Owner: "alice"}
			writeAuthPackage(t, sources.ExternalRoot, "demo", map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "mcp"}, map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"})
			pkg, err := sources.Load("demo")
			if err != nil {
				t.Fatal(err)
			}
			yes := true
			if _, err := pkg.UpdateConnection(&yes, &yes); err != nil {
				t.Fatal(err)
			}
			c := oauthCredential{Resource: "https://example.test/mcp", Config: oauth2.Config{ClientID: "client"}, Token: &oauth2.Token{AccessToken: "private-access", RefreshToken: "private-refresh"}, RevocationURL: endpoint.URL, RevocationAuthMethods: []string{"none"}}
			if scenario == "unsupported" {
				c.RevocationURL = ""
			}
			if err := saveResourceCredential(pkg.CredentialRoot(), pkg.ID, c); err != nil {
				t.Fatal(err)
			}
			m := New(t.Context(), sources, nil)
			result, err := m.Disconnect(t.Context(), pkg.ID)
			if err != nil || result.Bound || result.Enabled {
				t.Fatal(result, err)
			}
			want := "succeeded"
			if scenario == "failure" || scenario == "redirect" {
				want = "failed"
			}
			if scenario == "unsupported" {
				want = "unsupported"
			}
			if result.RemoteRevocation != want {
				t.Fatalf("%s: got %s", scenario, result.RemoteRevocation)
			}
			if want == "failed" && len(result.Warnings) == 0 {
				t.Fatal("remote failure not reported")
			}
			if CredentialReady(pkg.CredentialRoot(), pkg.ID, c.Resource) {
				t.Fatal("remote failure retained local token")
			}
			state, err := pkg.ReadConnection()
			if err != nil || state.Bound || state.Enabled {
				t.Fatal(state, err)
			}
			if leaked {
				t.Fatal("revocation followed redirect and leaked token")
			}
		})
	}
}
