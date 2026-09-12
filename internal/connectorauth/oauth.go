package connectorauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"agent-platform/internal/connector"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

type oauthSettings struct {
	Discovery           bool     `json:"discovery,omitempty"`
	ResourceMetadataURL string   `json:"resourceMetadataUrl,omitempty"`
	Scopes              []string `json:"scopes,omitempty"`
	ClientID            string   `json:"client_id,omitempty"`
	AuthorizationURL    string   `json:"authorization_endpoint,omitempty"`
	TokenURL            string   `json:"token_endpoint,omitempty"`
	Resource            string   `json:"resource,omitempty"`
	RedirectURI         string   `json:"redirect_uri,omitempty"`
}

func oauthResource(pkg connector.Package) (string, oauthSettings, error) {
	var settings oauthSettings
	if len(pkg.OAuth) > 0 {
		if err := connector.DecodeJSON(pkg.OAuth, &settings); err != nil {
			return "", settings, fmt.Errorf("invalid OAuth settings: %w", err)
		}
	}
	if pkg.AuthMode == connector.AuthOAuth {
		if settings.Discovery || settings.ResourceMetadataURL != "" {
			return "", settings, fmt.Errorf("MCP discovery belongs to auth_mode=mcp")
		}
		if strings.TrimSpace(settings.ClientID) == "" || !secureURL(settings.AuthorizationURL) || !secureURL(settings.TokenURL) || !secureURL(settings.Resource) {
			return "", settings, fmt.Errorf("oauth requires client_id, secure authorization_endpoint, token_endpoint and resource")
		}
	} else {
		if settings.AuthorizationURL != "" || settings.TokenURL != "" {
			return "", settings, fmt.Errorf("mcp discovers authorization and token endpoints")
		}
		if settings.Resource != "" && !secureURL(settings.Resource) {
			return "", settings, fmt.Errorf("invalid MCP OAuth resource")
		}
		if len(pkg.MCP) != 1 {
			return "", settings, fmt.Errorf("MCP OAuth requires one HTTP MCP component")
		}
	}
	if settings.RedirectURI != "" {
		if _, _, err := callbackAddress(settings.RedirectURI); err != nil {
			return "", settings, err
		}
	}
	if settings.ResourceMetadataURL != "" && !secureURL(settings.ResourceMetadataURL) {
		return "", settings, fmt.Errorf("invalid OAuth metadata URL")
	}
	if len(pkg.MCP) > 1 {
		return "", settings, fmt.Errorf("OAuth supports one resource per connector")
	}
	for _, component := range pkg.MCP {
		if platform, ok := component["platform"].(map[string]any); ok && platform["authSource"] != nil && platform["authSource"] != "" {
			return "", settings, fmt.Errorf("OAuth cannot combine with platform authSource")
		}
		resource, _ := component["url"].(string)
		if component["type"] != "streamableHttp" || !secureURL(resource) {
			return "", settings, fmt.Errorf("OAuth requires an HTTPS MCP resource")
		}
		for _, field := range []string{"headers", "staticHeaders"} {
			if headers, ok := component[field].(map[string]any); ok {
				for key := range headers {
					if strings.EqualFold(key, "Authorization") {
						return "", settings, fmt.Errorf("OAuth manages Authorization; remove the configured header")
					}
				}
			}
		}
		if settings.Resource != "" {
			return settings.Resource, settings, nil
		}
		return resource, settings, nil
	}
	return settings.Resource, settings, nil
}

// OAuthResource is the authorization audience; the MCP endpoint remains the
// independently frozen HTTP destination, including its path and query.
func OAuthResource(pkg connector.Package) (string, error) {
	resource, _, err := oauthResource(pkg)
	return resource, err
}

func oauthDestination(pkg connector.Package) string {
	for _, component := range pkg.MCP {
		destination, _ := component["url"].(string)
		return destination
	}
	var settings oauthSettings
	_ = connector.DecodeJSON(pkg.OAuth, &settings)
	return settings.Resource
}

// Only the local Platform process can receive this callback. A pre-registered
// client may require a fixed port; otherwise an ephemeral loopback port is used.
func callbackAddress(raw string) (address, path string, err error) {
	if raw == "" {
		return "127.0.0.1:0", "/callback", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Port() == "0" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
		return "", "", fmt.Errorf("redirect_uri must be an HTTP 127.0.0.1 URL with an explicit port and callback path")
	}
	return u.Host, u.Path, nil
}

func secureURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")
}

func (m *Manager) discover(ctx context.Context, resource, destination string, settings oauthSettings) (*oauthex.ProtectedResourceMetadata, *oauthex.AuthServerMeta, error) {
	metadata := settings.ResourceMetadataURL
	if metadata == "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, destination, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := m.client.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("OAuth discovery request failed")
		}
		resp.Body.Close()
		challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
		if err != nil {
			return nil, nil, fmt.Errorf("invalid OAuth challenge")
		}
		for _, c := range challenges {
			if strings.EqualFold(c.Scheme, "Bearer") {
				metadata = c.Params["resource_metadata"]
				if metadata != "" {
					break
				}
			}
		}
		if metadata == "" {
			u, _ := url.Parse(destination)
			u.Path = "/.well-known/oauth-protected-resource" + u.Path
			u.RawQuery = ""
			metadata = u.String()
		}
	}
	if !secureURL(metadata) {
		return nil, nil, fmt.Errorf("invalid OAuth resource metadata URL")
	}
	prm, err := oauthex.GetProtectedResourceMetadata(ctx, metadata, resource, m.client)
	if err != nil || prm == nil || len(prm.AuthorizationServers) == 0 {
		return nil, nil, fmt.Errorf("OAuth protected resource discovery failed")
	}
	if !secureURL(prm.AuthorizationServers[0]) {
		return nil, nil, fmt.Errorf("invalid OAuth issuer")
	}
	asm, err := sdkauth.GetAuthServerMetadata(ctx, prm.AuthorizationServers[0], m.client)
	if err != nil || asm == nil {
		return nil, nil, fmt.Errorf("OAuth authorization server discovery failed")
	}
	for _, u := range []string{asm.AuthorizationEndpoint, asm.TokenEndpoint} {
		if !secureURL(u) {
			return nil, nil, fmt.Errorf("OAuth server requires secure authorization and token endpoints")
		}
	}
	if settings.ClientID == "" && !secureURL(asm.RegistrationEndpoint) {
		return nil, nil, fmt.Errorf("MCP OAuth requires client_id or a secure registration endpoint")
	}
	if !slices.Contains(asm.CodeChallengeMethodsSupported, "S256") {
		return nil, nil, fmt.Errorf("OAuth server does not advertise PKCE S256")
	}
	return prm, asm, nil
}

func (m *Manager) loginOAuth(ctx context.Context, pkg connector.Package, s *login) error {
	resource, settings, err := oauthResource(pkg)
	if err != nil {
		return err
	}
	scopes := settings.Scopes
	cfg := oauth2.Config{ClientID: settings.ClientID, Endpoint: oauth2.Endpoint{AuthURL: settings.AuthorizationURL, TokenURL: settings.TokenURL, AuthStyle: oauth2.AuthStyleInParams}}
	var registrationEndpoint string
	if pkg.AuthMode == connector.AuthMCP {
		prm, asm, err := m.discover(ctx, resource, oauthDestination(pkg), settings)
		if err != nil {
			return err
		}
		if len(scopes) == 0 {
			scopes = prm.ScopesSupported
		}
		cfg.Endpoint.AuthURL, cfg.Endpoint.TokenURL = asm.AuthorizationEndpoint, asm.TokenEndpoint
		registrationEndpoint = asm.RegistrationEndpoint
	}
	address, callbackPath, err := callbackAddress(settings.RedirectURI)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("open OAuth loopback callback: %w", err)
	}
	defer listener.Close()
	redirect := settings.RedirectURI
	if redirect == "" {
		redirect = "http://" + listener.Addr().String() + callbackPath
	}
	if cfg.ClientID == "" {
		registration, err := oauthex.RegisterClient(ctx, registrationEndpoint, &oauthex.ClientRegistrationMetadata{
			ClientName: "Agent Platform", RedirectURIs: []string{redirect}, ApplicationType: "native",
			TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scope: strings.Join(scopes, " "),
		}, m.client)
		if err != nil {
			return fmt.Errorf("OAuth dynamic client registration failed")
		}
		if registration.ClientID == "" {
			return fmt.Errorf("OAuth registration returned no client id")
		}
		cfg.ClientID, cfg.ClientSecret = registration.ClientID, registration.ClientSecret
		if registration.TokenEndpointAuthMethod == "client_secret_basic" {
			cfg.Endpoint.AuthStyle = oauth2.AuthStyleInHeader
		}
	}
	cfg.RedirectURL, cfg.Scopes = redirect, scopes
	state, verifier := rand.Text(), oauth2.GenerateVerifier()
	result := make(chan string, 1)
	var received atomic.Bool
	callback := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet || r.URL.Path != callbackPath {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if r.URL.Query().Get("error") != "" {
			code = ""
		}
		if len(code) > 8192 {
			http.Error(w, "Invalid authorization code", 400)
			return
		}
		if received.CompareAndSwap(false, true) {
			result <- code
			io.WriteString(w, "Authorization received. You may return to Agent Platform.")
		} else {
			http.Error(w, "Callback already received", 409)
		}
	})}
	defer callback.Close()
	go callback.Serve(listener)
	authorizeOptions := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	exchangeOptions := []oauth2.AuthCodeOption{oauth2.VerifierOption(verifier)}
	if pkg.AuthMode == connector.AuthMCP {
		authorizeOptions = append(authorizeOptions, oauth2.SetAuthURLParam("resource", resource))
		exchangeOptions = append(exchangeOptions, oauth2.SetAuthURLParam("resource", resource))
	}
	m.setURL(s, cfg.AuthCodeURL(state, authorizeOptions...))
	var code string
	select {
	case <-ctx.Done():
		return ctx.Err()
	case code = <-result:
	}
	if code == "" {
		return fmt.Errorf("OAuth authorization was declined")
	}
	token, err := cfg.Exchange(context.WithValue(ctx, oauth2.HTTPClient, m.client), code, exchangeOptions...)
	if err != nil {
		return fmt.Errorf("OAuth token exchange failed; start login again")
	}
	if token.AccessToken == "" {
		return fmt.Errorf("OAuth returned an empty access token")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := lockCredentials(ctx, m.sources.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	defer unlock()
	return saveCredential(m.sources.PersistentRoot(), pkg.ID, oauthCredential{Resource: resource, Destination: oauthDestination(pkg), Config: cfg, Token: token})
}

// ValidatePackage checks local authentication declarations without network I/O.
func ValidatePackage(pkg connector.Package) error {
	if pkg.CLI != nil {
		if _, exists := pkg.CLI["versionCheck"]; exists {
			if _, err := cliSettingsFor(pkg); err != nil {
				return err
			}
		}
	}
	switch pkg.AuthMode {
	case "oauth", "mcp":
		_, _, err := oauthResource(pkg)
		return err
	case connector.AuthDelegated:
		if pkg.ManagedCLI() {
			_, err := cliSettingsFor(pkg)
			return err
		}
	}
	return nil
}
