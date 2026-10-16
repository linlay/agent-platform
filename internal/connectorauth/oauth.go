package connectorauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"agent-platform/internal/connector"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// oauthSettings is connector.json oauth for authMode mcp, camelCase like the
// rest of the manifest. Authorization and token endpoints are always discovered.
type oauthSettings struct {
	ResourceMetadataURL string   `json:"resourceMetadataUrl,omitempty"`
	Scopes              []string `json:"scopes,omitempty"`
	ClientID            string   `json:"clientId,omitempty"`
	Resource            string   `json:"resource,omitempty"`
	RedirectURI         string   `json:"redirectUri,omitempty"`
	ClientMetadataURL   string   `json:"clientMetadataUrl,omitempty"`
	Issuer              string   `json:"issuer,omitempty"`
}

// oauthResource validates MCP authorization for one selected HTTP component and
// returns its audience. It performs no network I/O.
func oauthResource(pkg connector.Package) (string, oauthSettings, error) {
	var settings oauthSettings
	if pkg.AuthMode != connector.AuthMCP {
		return "", settings, fmt.Errorf("MCP authorization requires authMode mcp, but connector %s uses %s", pkg.ID, pkg.AuthMode)
	}
	if len(pkg.OAuth) > 0 {
		if err := connector.DecodeJSON(pkg.OAuth, &settings); err != nil {
			return "", settings, fmt.Errorf("connector.json oauth is invalid: %w", err)
		}
	}
	if settings.Resource != "" && !secureURL(settings.Resource) {
		return "", settings, fmt.Errorf("oauth.resource %q must be an HTTPS URL (HTTP only for localhost)", redactURLString(settings.Resource))
	}
	if settings.RedirectURI != "" {
		if _, _, err := callbackAddress(settings.RedirectURI); err != nil {
			return "", settings, fmt.Errorf("oauth.%w", err)
		}
	}
	if settings.ResourceMetadataURL != "" && !secureURL(settings.ResourceMetadataURL) {
		return "", settings, fmt.Errorf("oauth.resourceMetadataUrl %q must be an HTTPS URL (HTTP only for localhost)", redactURLString(settings.ResourceMetadataURL))
	}
	switch len(pkg.MCP) {
	case 0:
		return "", settings, fmt.Errorf("MCP authorization requires an HTTP MCP component; connector %s has none (CLI-only and standalone OAuth are not supported)", pkg.ID)
	case 1:
	default:
		return "", settings, fmt.Errorf("MCP authorization applies to one component at a time; select one of the %d MCP components", len(pkg.MCP))
	}
	for name, component := range pkg.MCP {
		if platform, ok := component["platform"].(map[string]any); ok && platform["authSource"] != nil && platform["authSource"] != "" {
			return "", settings, fmt.Errorf("MCP component %s cannot combine MCP authorization with platform.authSource", name)
		}
		if component["type"] != "streamableHttp" {
			return "", settings, fmt.Errorf("MCP component %s uses type %v; MCP authorization requires a streamableHttp component", name, component["type"])
		}
		resource, _ := component["url"].(string)
		if !secureURL(resource) {
			return "", settings, fmt.Errorf("MCP component %s url %q must be HTTPS for MCP authorization (HTTP only for localhost)", name, redactURLString(resource))
		}
		if headers, ok := component["headers"].(map[string]any); ok {
			for key := range headers {
				if strings.EqualFold(key, "Authorization") {
					return "", settings, fmt.Errorf("MCP component %s declares an Authorization header; MCP authorization manages it, remove the header", name)
				}
			}
		}
		if settings.Resource != "" {
			return settings.Resource, settings, nil
		}
		return resource, settings, nil
	}
	return "", settings, nil
}

// authorizationScopes is the scope request of both login and the discovery
// check: a pending step-up from an insufficient_scope challenge first, then the
// initial Bearer challenge as the MCP specification requires, then the
// connector's configured scopes and finally scopes_supported.
func authorizationScopes(pkg connector.Package, resource string, settings oauthSettings, found *discovered) []string {
	if existing, err := readCredential(pkg.PersistentRoot(), pkg.ID); err == nil {
		if grant, ok := selectCredential(existing, resource, []string{oauthDestination(pkg)}); ok && len(grant.RequiredScopes) > 0 {
			return grant.RequiredScopes
		}
	}
	switch {
	case len(found.challengedScopes) > 0:
		return found.challengedScopes
	case len(settings.Scopes) > 0:
		return settings.Scopes
	}
	return found.prm.ScopesSupported
}

// mcpAuthorizationSettings adds the deployment-owned client registration, as
// login does, so discovery checks evaluate the same client choice.
func mcpAuthorizationSettings(pkg connector.Package) (string, oauthSettings, error) {
	resource, settings, err := oauthResource(pkg)
	if err != nil {
		return "", settings, err
	}
	clientInfo, err := readClientInfo(pkg)
	if err != nil {
		return "", settings, fmt.Errorf("stored OAuth client information is unreadable: %w", err)
	}
	if clientInfo.ClientID != "" {
		settings.ClientID = clientInfo.ClientID
		settings.Issuer = clientInfo.Issuer
		if clientInfo.RedirectURI != "" {
			settings.RedirectURI = clientInfo.RedirectURI
		}
	}
	return resource, settings, nil
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
		if destination != "" {
			return destination
		}
	}
	return ""
}

// Only the local Platform process can receive this callback. A pre-registered
// client may require a fixed port; otherwise an ephemeral loopback port is used.
func callbackAddress(raw string) (address, path string, err error) {
	if raw == "" {
		return "127.0.0.1:0", "/callback", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Port() == "0" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
		return "", "", fmt.Errorf("redirectUri must be an HTTP 127.0.0.1 URL with an explicit port and callback path")
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

func (m *Manager) loginOAuth(ctx context.Context, pkg connector.Package, s *login) error {
	initialState, err := readAuthState(m.sources.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	if s.generation != nil {
		initialState.Generation = *s.generation
	}
	resource, settings, err := mcpAuthorizationSettings(pkg)
	if err != nil {
		return err
	}
	clientInfo, err := readClientInfo(pkg)
	if err != nil {
		return err
	}
	var component map[string]any
	for _, value := range pkg.MCP {
		component = value
	}
	found, err := discoverAuthorization(ctx, m.client, resource, oauthDestination(pkg), probeHeaders(component), settings)
	if err != nil {
		return err
	}
	prm, asm := found.prm, found.asm
	scopes := authorizationScopes(pkg, resource, settings, found)
	cfg := oauth2.Config{ClientID: settings.ClientID, Endpoint: oauth2.Endpoint{AuthURL: asm.AuthorizationEndpoint, TokenURL: asm.TokenEndpoint, AuthStyle: oauth2.AuthStyleInParams}}
	cfg.ClientSecret = clientInfo.ClientSecret
	if cfg.ClientSecret != "" {
		cfg.Endpoint.AuthStyle = oauth2.AuthStyleInHeader
	}
	if clientInfo.AuthMethod == "client_secret_post" {
		cfg.Endpoint.AuthStyle = oauth2.AuthStyleInParams
	}
	discoveredIssuer := asm.Issuer
	registrationGrants := []string{"authorization_code", "refresh_token"}
	// Preserve refresh registration for servers that omit this optional field.
	if asm.GrantTypesSupported != nil && !slices.Contains(asm.GrantTypesSupported, "refresh_token") {
		registrationGrants = []string{"authorization_code"}
	}
	dir, err := StateDir(pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	if err := savePrivateJSON(filepath.Join(dir, "discovery-"+grantKey(resource, oauthDestination(pkg))+".json"), map[string]any{"resource": prm, "authorizationServer": asm}); err != nil {
		return err
	}
	if cfg.ClientID == "" && found.registration == "client_metadata" {
		if err := validateClientMetadata(ctx, m.client, settings.ClientMetadataURL, settings.RedirectURI); err != nil {
			return err
		}
		cfg.ClientID = settings.ClientMetadataURL
	}
	registrationEndpoint := asm.RegistrationEndpoint
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
			TokenEndpointAuthMethod: "none", GrantTypes: registrationGrants, ResponseTypes: []string{"code"}, Scope: strings.Join(scopes, " "),
		}, m.client)
		if err != nil {
			return oauthRegistrationError(err)
		}
		if registration.ClientID == "" {
			return fmt.Errorf("OAuth dynamic client registration at %s returned no client_id", registrationEndpoint)
		}
		cfg.ClientID, cfg.ClientSecret = registration.ClientID, registration.ClientSecret
		if registration.TokenEndpointAuthMethod != "" && registration.TokenEndpointAuthMethod != "none" && registration.TokenEndpointAuthMethod != "client_secret_basic" && registration.TokenEndpointAuthMethod != "client_secret_post" {
			return fmt.Errorf("OAuth dynamic client registration returned unsupported token_endpoint_auth_method %q", registration.TokenEndpointAuthMethod)
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
	authorizeOptions = append(authorizeOptions, oauth2.SetAuthURLParam("resource", resource))
	exchangeOptions = append(exchangeOptions, oauth2.SetAuthURLParam("resource", resource))
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
	token, err := cfg.Exchange(context.WithValue(ctx, oauth2.HTTPClient, tokenHTTPClient(m.client, cfg.Endpoint.TokenURL, resource)), code, exchangeOptions...)
	if err != nil {
		return oauthExchangeError(cfg.Endpoint.TokenURL, err)
	}
	if token.AccessToken == "" {
		return fmt.Errorf("OAuth token endpoint %s returned an empty access_token", cfg.Endpoint.TokenURL)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := lockCredentials(ctx, m.sources.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	defer unlock()
	currentState, err := readAuthState(m.sources.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	if currentState.Generation != initialState.Generation || ctx.Err() != nil {
		return fmt.Errorf("authorization was canceled or signed out")
	}
	return saveCredential(m.sources.PersistentRoot(), pkg.ID, oauthCredential{Generation: currentState.Generation, MCP: true, Resource: resource, Destination: oauthDestination(pkg), Config: cfg, Token: token})
}

func oauthRegistrationError(err error) error {
	message := "OAuth dynamic client registration failed"
	var registrationError *oauthex.ClientRegistrationError
	if errors.As(err, &registrationError) {
		if code := oauthRegistrationErrorDetail(registrationError.ErrorCode, 128); code != "" {
			message += ": " + code
		}
		if description := oauthRegistrationErrorDetail(registrationError.ErrorDescription, 512); description != "" {
			message += ": " + description
		}
	}
	// Other SDK errors can contain entire upstream response bodies.
	return errors.New(message)
}

// oauthExchangeError reports the OAuth error code and description but never the
// raw response body, which may echo codes or tokens.
func oauthExchangeError(endpoint string, err error) error {
	message := "OAuth token exchange at " + redactURLString(endpoint) + " failed"
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) {
		if retrieve.Response != nil {
			message += fmt.Sprintf(" with HTTP %d", retrieve.Response.StatusCode)
		}
		if code := oauthRegistrationErrorDetail(retrieve.ErrorCode, 128); code != "" {
			message += ": " + code
		}
		if description := oauthRegistrationErrorDetail(retrieve.ErrorDescription, 512); description != "" {
			message += ": " + description
		}
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		message += ": " + redactKnown(err.Error(), endpoint)
	}
	return errors.New(message + "; start login again")
}

func oauthRegistrationErrorDetail(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return value
}

// ValidatePackage checks local authentication declarations without network I/O.
func ValidatePackage(pkg connector.Package) error {
	if pkg.CLI != nil {
		if _, err := cliSettingsFor(pkg); err != nil {
			return err
		}
	}
	switch pkg.AuthMode {
	case connector.AuthMCP:
		if len(pkg.OAuth) > 0 {
			var settings oauthSettings
			if err := connector.DecodeJSON(pkg.OAuth, &settings); err != nil {
				return fmt.Errorf("connector.json oauth is invalid: %w", err)
			}
		}
		if templates, err := pkg.CLICredentialTemplates(); err != nil {
			return err
		} else if len(templates) > 0 {
			selected, err := cliOAuthComponent(pkg)
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
	}
	return nil
}
