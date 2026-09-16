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
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"agent-platform/internal/connector"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

type oauthSettings struct {
	Issuer              string   `json:"issuer,omitempty"`
	RevocationURL       string   `json:"revocation_endpoint,omitempty"`
	Discovery           bool     `json:"discovery,omitempty"`
	ResourceMetadataURL string   `json:"resourceMetadataUrl,omitempty"`
	Scopes              []string `json:"scopes,omitempty"`
	ClientID            string   `json:"client_id,omitempty"`
	AuthorizationURL    string   `json:"authorization_endpoint,omitempty"`
	TokenURL            string   `json:"token_endpoint,omitempty"`
	Resource            string   `json:"resource,omitempty"`
	RedirectURI         string   `json:"redirect_uri,omitempty"`
	ClientMetadataURL   string   `json:"client_metadata_url,omitempty"`
}

func oauthSingleResource(pkg connector.Package) (string, oauthSettings, error) {
	var settings oauthSettings
	if len(pkg.OAuth) > 0 {
		if err := connector.DecodeJSON(pkg.OAuth, &settings); err != nil {
			return "", settings, fmt.Errorf("invalid OAuth settings: %w", err)
		}
	}
	if pkg.AuthMode == connector.AuthOAuth {
		if settings.Discovery || settings.ResourceMetadataURL != "" || settings.ClientMetadataURL != "" {
			return "", settings, fmt.Errorf("MCP discovery belongs to auth_mode=mcp")
		}
		if (settings.Issuer != "" && !secureURL(settings.Issuer)) || (settings.RevocationURL != "" && !secureURL(settings.RevocationURL)) {
			return "", settings, fmt.Errorf("oauth issuer and revocation endpoint must use HTTPS")
		}
		if (settings.AuthorizationURL == "") != (settings.TokenURL == "") {
			return "", settings, fmt.Errorf("oauth authorization and token endpoints must be supplied together")
		}
		if !secureURL(settings.Resource) || (settings.Issuer == "" && (strings.TrimSpace(settings.ClientID) == "" || !secureURL(settings.AuthorizationURL) || !secureURL(settings.TokenURL))) || (settings.AuthorizationURL != "" && (!secureURL(settings.AuthorizationURL) || !secureURL(settings.TokenURL))) {
			return "", settings, fmt.Errorf("oauth requires client_id, secure authorization_endpoint, token_endpoint and resource")
		}
	} else {
		if settings.AuthorizationURL != "" || settings.TokenURL != "" {
			return "", settings, fmt.Errorf("mcp discovers authorization and token endpoints")
		}
		if settings.Resource != "" && !secureURL(settings.Resource) {
			return "", settings, fmt.Errorf("invalid MCP OAuth resource")
		}
		if len(pkg.MCP) == 0 {
			return "", settings, fmt.Errorf("MCP OAuth requires an HTTP MCP component")
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
	for _, component := range pkg.MCP {
		if platform, ok := component["platform"].(map[string]any); ok && platform["authSource"] != nil && platform["authSource"] != "" {
			return "", settings, fmt.Errorf("OAuth cannot combine with platform authSource")
		}
		if component["type"] == "stdio" && pkg.AuthMode == connector.AuthOAuth {
			return settings.Resource, settings, nil
		}
		resource, _ := component["url"].(string)
		if (component["type"] != "streamableHttp" && component["type"] != "http") || !secureURL(resource) {
			return "", settings, fmt.Errorf("OAuth requires an HTTPS MCP resource")
		}
		for _, field := range []string{"headers", "staticHeaders"} {
			if headers, ok := component[field].(map[string]any); ok {
				for key := range headers {
					if strings.EqualFold(key, "Authorization") && !(field == "headers" && headers[key] == "Bearer ${OAUTH_ACCESS_TOKEN}") {
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

type oauthTarget struct {
	Component   string
	Resource    string
	Destination string
	Settings    oauthSettings
}

// Each HTTP component is an independent audience/destination binding. Explicit
// oauth.resource changes the audience only; it never broadens the destination.
func oauthTargets(pkg connector.Package) ([]oauthTarget, error) {
	names := make([]string, 0, len(pkg.MCP))
	for name := range pkg.MCP {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		resource, settings, err := oauthSingleResource(pkg)
		if err != nil {
			return nil, err
		}
		return []oauthTarget{{Resource: resource, Destination: resource, Settings: settings}}, nil
	}
	targets := make([]oauthTarget, 0, len(names))
	for _, name := range names {
		single, err := OAuthComponent(pkg, name)
		if err != nil {
			return nil, err
		}
		resource, settings, err := oauthSingleResource(single)
		if err != nil {
			return nil, fmt.Errorf("MCP component %s: %w", name, err)
		}
		destination, _ := pkg.MCP[name]["url"].(string)
		targets = append(targets, oauthTarget{Component: name, Resource: resource, Destination: destination, Settings: settings})
	}
	return targets, nil
}

func oauthResource(pkg connector.Package) (string, oauthSettings, error) {
	targets, err := oauthTargets(pkg)
	if err != nil {
		return "", oauthSettings{}, err
	}
	if len(targets) != 1 {
		return "", oauthSettings{}, fmt.Errorf("select an OAuth MCP component resource")
	}
	return targets[0].Resource, targets[0].Settings, nil
}

func OAuthResource(pkg connector.Package) (string, error) {
	resource, _, err := oauthResource(pkg)
	return resource, err
}

func OAuthResourceForComponent(pkg connector.Package, name string) (string, error) {
	targets, err := oauthTargets(pkg)
	if err != nil {
		return "", err
	}
	for _, target := range targets {
		if target.Component == name {
			return target.Resource, nil
		}
	}
	return "", fmt.Errorf("OAuth MCP component not found")
}

func oauthDestination(pkg connector.Package) string {
	targets, err := oauthTargets(pkg)
	if err != nil || len(targets) != 1 {
		return ""
	}
	return targets[0].Destination
}

func oauthPackageReady(pkg connector.Package) (bool, error) {
	targets, err := oauthTargets(pkg)
	if err != nil {
		return false, err
	}
	for _, target := range targets {
		if !CredentialReady(pkg.CredentialRoot(), pkg.ID, target.Resource, target.Destination) {
			return false, nil
		}
	}
	return true, nil
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

func (m *Manager) discover(ctx context.Context, resource, destination string, settings oauthSettings, scopeOutput ...*[]string) (*oauthex.ProtectedResourceMetadata, *oauthex.AuthServerMeta, error) {
	metadata := settings.ResourceMetadataURL
	fallback := false
	var challengedScopes []string
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
				if scope := c.Params["scope"]; scope != "" {
					challengedScopes = strings.Fields(scope)
				}
				metadata = c.Params["resource_metadata"]
				if metadata != "" {
					break
				}
			}
		}
		if metadata == "" {
			fallback = true
			u, _ := url.Parse(destination)
			u.Path = "/.well-known/oauth-protected-resource" + u.Path
			u.RawQuery = ""
			metadata = u.String()
		}
	}
	if !secureURL(metadata) {
		return nil, nil, fmt.Errorf("invalid OAuth resource metadata URL")
	}
	prm, missing, err := m.resourceMetadata(ctx, metadata, resource)
	if missing && fallback {
		u, _ := url.Parse(destination)
		u.Path = "/.well-known/oauth-protected-resource"
		u.RawPath = ""
		u.RawQuery = ""
		prm, _, err = m.resourceMetadata(ctx, u.String(), resource)
	}
	if err != nil || prm == nil || len(prm.AuthorizationServers) == 0 {
		return nil, nil, fmt.Errorf("OAuth protected resource discovery failed")
	}
	issuer := prm.AuthorizationServers[0]
	if settings.Issuer != "" {
		if !slices.Contains(prm.AuthorizationServers, settings.Issuer) {
			return nil, nil, fmt.Errorf("configured issuer is not advertised by resource")
		}
		issuer = settings.Issuer
	}
	if !secureURL(issuer) {
		return nil, nil, fmt.Errorf("invalid OAuth issuer")
	}
	asm, err := sdkauth.GetAuthServerMetadata(ctx, issuer, m.client)
	if err != nil || asm == nil {
		return nil, nil, fmt.Errorf("OAuth authorization server discovery failed")
	}
	for _, u := range []string{asm.AuthorizationEndpoint, asm.TokenEndpoint} {
		if !secureURL(u) {
			return nil, nil, fmt.Errorf("OAuth server requires secure authorization and token endpoints")
		}
	}
	if settings.ClientID == "" && !(asm.ClientIDMetadataDocumentSupported && settings.ClientMetadataURL != "") && !secureURL(asm.RegistrationEndpoint) {
		return nil, nil, fmt.Errorf("MCP OAuth requires registered client information; configure OAuth client credentials")
	}
	if !slices.Contains(asm.CodeChallengeMethodsSupported, "S256") {
		return nil, nil, fmt.Errorf("OAuth server does not advertise PKCE S256")
	}
	if len(scopeOutput) > 0 && scopeOutput[0] != nil {
		*scopeOutput[0] = challengedScopes
	}
	return prm, asm, nil
}

func (m *Manager) loginOAuth(ctx context.Context, pkg connector.Package, s *login) error {
	targets, err := oauthTargets(pkg)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if len(targets) > 1 && CredentialReady(pkg.CredentialRoot(), pkg.ID, target.Resource, target.Destination) {
			continue
		}
		m.mu.Lock()
		s.URL = ""
		s.Status = "preparing"
		s.Message = "Preparing authorization for " + target.Component
		m.mu.Unlock()
		selected := pkg
		if target.Component != "" {
			selected, err = OAuthComponent(pkg, target.Component)
			if err != nil {
				return err
			}
		}
		if err := m.loginOAuthTarget(ctx, selected, s, target); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) loginOAuthTarget(ctx context.Context, pkg connector.Package, s *login, target oauthTarget) error {
	initialState, err := readAuthState(m.credentialRoot(), pkg.ID)
	if err != nil {
		return err
	}
	if s.generation != nil {
		initialState.Generation = *s.generation
	}
	resource, settings := target.Resource, target.Settings
	clientInfo, err := readClientInfo(pkg)
	if err != nil {
		return err
	}
	if clientInfo.ClientID != "" {
		settings.ClientID = clientInfo.ClientID
		settings.Issuer = clientInfo.Issuer
		if clientInfo.RedirectURI != "" {
			settings.RedirectURI = clientInfo.RedirectURI
		}
	}
	scopes := settings.Scopes
	stepUp := false
	if existing, readErr := readCredential(pkg.CredentialRoot(), pkg.ID); readErr == nil {
		if grant, ok := selectCredential(existing, resource, []string{oauthDestination(pkg)}); ok && len(grant.RequiredScopes) > 0 {
			scopes = grant.RequiredScopes
			stepUp = true
		}
	}
	cfg := oauth2.Config{ClientID: settings.ClientID, Endpoint: oauth2.Endpoint{AuthURL: settings.AuthorizationURL, TokenURL: settings.TokenURL, AuthStyle: oauth2.AuthStyleInParams}}
	cfg.ClientSecret = clientInfo.ClientSecret
	if cfg.ClientSecret != "" {
		cfg.Endpoint.AuthStyle = oauth2.AuthStyleInHeader
	}
	if clientInfo.AuthMethod == "client_secret_post" {
		cfg.Endpoint.AuthStyle = oauth2.AuthStyleInParams
	}
	var discoveredIssuer string
	var registrationEndpoint string
	var revocationURL string
	var revocationMethods []string
	if pkg.AuthMode == connector.AuthOAuth {
		revocationURL = settings.RevocationURL
		if settings.Issuer != "" && (cfg.Endpoint.AuthURL == "" || cfg.ClientID == "") {
			asm, err := sdkauth.GetAuthServerMetadata(ctx, settings.Issuer, m.client)
			if err != nil || asm == nil || asm.Issuer != settings.Issuer {
				return fmt.Errorf("OAuth issuer discovery failed")
			}
			if cfg.Endpoint.AuthURL == "" {
				if !secureURL(asm.AuthorizationEndpoint) || !secureURL(asm.TokenEndpoint) {
					return fmt.Errorf("OAuth endpoints must use HTTPS")
				}
				cfg.Endpoint.AuthURL, cfg.Endpoint.TokenURL = asm.AuthorizationEndpoint, asm.TokenEndpoint
			}
			discoveredIssuer = asm.Issuer
			registrationEndpoint = asm.RegistrationEndpoint
			if cfg.ClientID == "" && !secureURL(registrationEndpoint) {
				return fmt.Errorf("OAuth client configuration is required")
			}
			if revocationURL == "" && secureURL(asm.RevocationEndpoint) {
				revocationURL = asm.RevocationEndpoint
				revocationMethods = asm.RevocationEndpointAuthMethodsSupported
			}
		}
	}
	if pkg.AuthMode == connector.AuthMCP {
		var requiredScopes []string
		prm, asm, err := m.discover(ctx, resource, target.Destination, settings, &requiredScopes)
		if err != nil {
			return err
		}
		if len(requiredScopes) > 0 && !stepUp {
			scopes = requiredScopes
		}
		if len(scopes) == 0 {
			scopes = prm.ScopesSupported
		}
		cfg.Endpoint.AuthURL, cfg.Endpoint.TokenURL = asm.AuthorizationEndpoint, asm.TokenEndpoint
		discoveredIssuer = asm.Issuer
		dir, dirErr := StateDir(pkg.CredentialRoot(), pkg.ID)
		if dirErr != nil {
			return dirErr
		}
		if err := savePrivateJSON(filepath.Join(dir, "discovery-"+grantKey(resource, oauthDestination(pkg))+".json"), map[string]any{"resource": prm, "authorizationServer": asm}); err != nil {
			return err
		}
		if cfg.ClientID == "" && asm.ClientIDMetadataDocumentSupported && settings.ClientMetadataURL != "" {
			if err := validateClientMetadata(ctx, m.client, settings.ClientMetadataURL, settings.RedirectURI); err != nil {
				return err
			}
			cfg.ClientID = settings.ClientMetadataURL
		}
		registrationEndpoint = asm.RegistrationEndpoint
		if secureURL(asm.RevocationEndpoint) {
			revocationURL = asm.RevocationEndpoint
			revocationMethods = asm.RevocationEndpointAuthMethodsSupported
		}
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
		if registration.TokenEndpointAuthMethod != "" && registration.TokenEndpointAuthMethod != "none" && registration.TokenEndpointAuthMethod != "client_secret_basic" && registration.TokenEndpointAuthMethod != "client_secret_post" {
			return fmt.Errorf("unsupported registered client authentication method")
		}
		if registration.TokenEndpointAuthMethod == "client_secret_basic" {
			cfg.Endpoint.AuthStyle = oauth2.AuthStyleInHeader
		}
		if registration.TokenEndpointAuthMethod == "none" {
			cfg.ClientSecret = ""
		}
		if err := saveRegisteredClient(ctx, pkg, OAuthClientInfo{Issuer: discoveredIssuer, ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURI: redirect, AuthMethod: registration.TokenEndpointAuthMethod}, initialState.Generation); err != nil {
			return err
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
	if pkg.AuthMode == connector.AuthMCP || settings.Issuer != "" {
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
	exchangeResource := ""
	if pkg.AuthMode == connector.AuthMCP {
		exchangeResource = resource
	}
	token, err := cfg.Exchange(context.WithValue(ctx, oauth2.HTTPClient, tokenHTTPClient(m.client, cfg.Endpoint.TokenURL, exchangeResource)), code, exchangeOptions...)
	if err != nil {
		return fmt.Errorf("OAuth token exchange failed; start login again")
	}
	if token.AccessToken == "" {
		return fmt.Errorf("OAuth returned an empty access token")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := lockCredentials(ctx, m.credentialRoot(), pkg.ID)
	if err != nil {
		return err
	}
	defer unlock()
	currentState, err := readAuthState(m.credentialRoot(), pkg.ID)
	if err != nil {
		return err
	}
	if currentState.Generation != initialState.Generation || ctx.Err() != nil {
		return fmt.Errorf("authorization was canceled or signed out")
	}
	credential := oauthCredential{Generation: currentState.Generation, MCP: pkg.AuthMode == connector.AuthMCP, Resource: resource, Destination: target.Destination, Config: cfg, Token: token, RevocationURL: revocationURL, RevocationAuthMethods: revocationMethods}
	return saveCredential(m.credentialRoot(), pkg.ID, credential)
}

// ValidatePackage checks local authentication declarations without network I/O.
func ValidatePackage(pkg connector.Package) error {
	if err := pkg.ValidateAuthBindings(); err != nil {
		return err
	}
	if pkg.CLI != nil {
		if _, exists := pkg.CLI["versionCheck"]; exists {
			if _, err := cliSettingsFor(pkg); err != nil {
				return err
			}
		}
	}
	switch pkg.AuthMode {
	case "oauth", "mcp":
		if len(pkg.OAuth) > 0 {
			var settings oauthSettings
			if err := connector.DecodeJSON(pkg.OAuth, &settings); err != nil {
				return fmt.Errorf("invalid OAuth settings")
			}
		}
		if binding := pkg.AuthBindings["cli"]; pkg.CLI != nil && (len(binding.OAuth) > 0 || binding.Grant != "" || len(binding.Env) > 0) {
			selected, err := OAuthComponent(pkg, "cli")
			if err != nil {
				return err
			}
			if _, _, err := oauthResource(selected); err != nil {
				return err
			}
		}
		if len(pkg.MCP) == 0 {
			_, _, err := oauthResource(pkg)
			return err
		}
		for name := range pkg.MCP {
			selected, err := OAuthComponent(pkg, name)
			if err != nil {
				return err
			}
			if _, _, err := oauthResource(selected); err != nil {
				return err
			}
		}
		return nil
	case connector.AuthDelegated:
		if pkg.ManagedCLI() {
			_, err := cliSettingsFor(pkg)
			return err
		}
	}
	return nil
}
