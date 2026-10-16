package connectorauth

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/connector"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// discoveryProtocolVersion is only used for the unauthenticated initialize
// probe; the MCP client negotiates the session version independently.
const discoveryProtocolVersion = "2025-11-25"

// DiscoveryStep records one discovery request. It never contains credentials,
// configured header values or response bodies.
type DiscoveryStep struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Status int    `json:"status,omitempty"`
	Result string `json:"result,omitempty"`
}

func (s DiscoveryStep) String() string {
	text := s.Method + " " + s.URL + " -> "
	if s.Status != 0 {
		text += fmt.Sprint(s.Status)
	} else {
		text += "no response"
	}
	if s.Result != "" {
		text += " (" + s.Result + ")"
	}
	return text
}

// DiscoveryReport is the outcome of checking, without signing in, whether MCP
// authorization can start for one component.
type DiscoveryReport struct {
	Status              string          `json:"status"`
	Code                string          `json:"code,omitempty"`
	Message             string          `json:"message,omitempty"`
	ComponentID         string          `json:"componentId,omitempty"`
	ResourceMetadataURL string          `json:"resourceMetadataUrl,omitempty"`
	Resource            string          `json:"resource,omitempty"`
	Issuer              string          `json:"issuer,omitempty"`
	ClientRegistration  string          `json:"clientRegistration,omitempty"`
	Scopes              []string        `json:"scopes,omitempty"`
	Steps               []DiscoveryStep `json:"steps"`
}

// DiscoveryError keeps a stable code for callers that must not show endpoints,
// while Error() lists every request so the cause is visible in login status.
type DiscoveryError struct {
	Code    string
	Message string
	Steps   []DiscoveryStep
}

func (e *DiscoveryError) Error() string {
	if len(e.Steps) == 0 {
		return e.Message
	}
	steps := make([]string, len(e.Steps))
	for i, step := range e.Steps {
		steps[i] = step.String()
	}
	return e.Message + "; requests: " + strings.Join(steps, "; ")
}

type discovered struct {
	metadataURL      string
	prm              *oauthex.ProtectedResourceMetadata
	asm              *oauthex.AuthServerMeta
	challengedScopes []string
	registration     string
	steps            []DiscoveryStep
}

// discoveryRecorder records each request and refuses insecure destinations,
// including redirects, before any network I/O.
type discoveryRecorder struct {
	base  http.RoundTripper
	known []string
	mu    sync.Mutex
	steps []DiscoveryStep
}

func (t *discoveryRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	step := DiscoveryStep{Method: req.Method, URL: redactedURL(req.URL)}
	if !secureURL(req.URL.String()) {
		step.Result = "refused: discovery requires HTTPS (HTTP only for localhost)"
		t.add(step)
		return nil, fmt.Errorf("insecure discovery destination %s", step.URL)
	}
	response, err := t.base.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		known := append(slices.Clone(t.known), req.URL.String())
		t.mu.Unlock()
		step.Result = "request failed: " + redactKnown(err.Error(), known...)
	} else {
		step.Status = response.StatusCode
	}
	t.add(step)
	return response, err
}

func (t *discoveryRecorder) add(step DiscoveryStep) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.steps = append(t.steps, step)
}

func (t *discoveryRecorder) snapshot() []DiscoveryStep {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.steps)
}

func (t *discoveryRecorder) annotateLast(method, result string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.steps) - 1; i >= 0; i-- {
		if t.steps[i].Method == method {
			if t.steps[i].Result == "" {
				t.steps[i].Result = result
			}
			return
		}
	}
}

// redactedURL drops query strings, which may carry gateway keys.
func redactedURL(u *url.URL) string {
	clean := *u
	clean.User, clean.RawQuery, clean.ForceQuery, clean.Fragment = nil, "", false, ""
	return clean.String()
}

// redactURLString parses a known URL and drops userinfo, query and fragment,
// keeping a marker where a query was removed. Unparseable text keeps only its
// scheme: its authority may still hold userinfo credentials.
func redactURLString(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		if scheme, _, ok := strings.Cut(raw, "://"); ok && !strings.ContainsAny(scheme, "/?#@") {
			return scheme + "://[unparseable URL redacted]"
		}
		return "[unparseable URL redacted]"
	}
	clean := redactedURL(u)
	if u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "?") {
		clean += "?[redacted]"
	}
	return clean
}

// diagnosticURL matches any URL left in free text. Host and path stop at quotes
// so formatted URLs keep their delimiters; a query or fragment extends to the
// next whitespace, since quotes and brackets are valid inside a query.
var diagnosticURL = regexp.MustCompile(`(?i)(https?://)(?:[^\s/?#@]*@)?([^\s"'<>?#]*)([?#]\S*)?`)

// redactDiagnostic removes userinfo, query strings and fragments from every
// URL in diagnostic text. Endpoint, metadata and SDK error text may embed
// connector URLs whose query carries an API key.
func redactDiagnostic(text string) string {
	return diagnosticURL.ReplaceAllStringFunc(text, func(match string) string {
		parts := diagnosticURL.FindStringSubmatch(match)
		redacted := parts[1] + parts[2]
		if parts[3] != "" {
			redacted += "?[redacted]"
		}
		return redacted
	})
}

// redactKnown replaces known URLs, raw or %q-escaped as in url.Error, with
// their parsed redaction before the generic fallback runs.
func redactKnown(text string, known ...string) string {
	known = slices.Clone(known)
	slices.SortFunc(known, func(a, b string) int { return len(b) - len(a) })
	for _, raw := range known {
		if raw == "" {
			continue
		}
		clean := redactURLString(raw)
		quoted := strconv.Quote(raw)
		text = strings.ReplaceAll(text, quoted[1:len(quoted)-1], clean)
		text = strings.ReplaceAll(text, raw, clean)
	}
	return redactDiagnostic(text)
}

// discoverAuthorization follows the MCP authorization specification: an MCP
// request without a token yields a Bearer challenge that names the protected
// resource metadata. Some servers answer GET with 200 or 405, so the probe is a
// real initialize POST; well-known paths are only a fallback.
func discoverAuthorization(ctx context.Context, client *http.Client, resource, destination string, headers map[string]string, settings oauthSettings) (*discovered, error) {
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	known := []string{destination, resource, settings.ResourceMetadataURL, settings.Issuer, settings.ClientMetadataURL}
	recorder := &discoveryRecorder{base: base, known: known}
	recording := *client
	recording.Transport = recorder
	fail := func(code, format string, args ...any) (*discovered, error) {
		return nil, &DiscoveryError{Code: code, Message: redactKnown(fmt.Sprintf(format, args...), known...), Steps: recorder.snapshot()}
	}
	// URLs learned during discovery are redacted the same way as configured ones.
	addKnown := func(urls ...string) {
		known = append(known, urls...)
		recorder.mu.Lock()
		recorder.known = slices.Clone(known)
		recorder.mu.Unlock()
	}
	result := &discovered{}
	metadata := settings.ResourceMetadataURL
	if metadata != "" && !secureURL(metadata) {
		return fail("invalid_metadata_url", "oauth.resourceMetadataUrl %q must use HTTPS (HTTP only for localhost)", metadata)
	}
	var candidates []string
	challengeFound := false
	if metadata == "" {
		challenge, scopes, err := probeChallenge(ctx, &recording, recorder, destination, headers)
		if err != nil {
			return fail("discovery_request_failed", "MCP authorization discovery could not reach the MCP endpoint %s: %v", destination, err)
		}
		result.challengedScopes = scopes
		if challenge != "" {
			challengeFound = true
			candidates = []string{challenge}
			addKnown(challenge)
		} else {
			candidates = wellKnownResourceMetadata(destination)
		}
	} else {
		candidates = []string{metadata}
	}
	for i, candidate := range candidates {
		if !secureURL(candidate) {
			return fail("invalid_metadata_url", "resource metadata URL %q from the Bearer challenge must use HTTPS (HTTP only for localhost)", candidate)
		}
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, candidate, resource, &recording)
		if err == nil && prm != nil {
			result.metadataURL, result.prm = candidate, prm
			break
		}
		steps := recorder.snapshot()
		status := 0
		if len(steps) > 0 {
			status = steps[len(steps)-1].Status
		}
		if status >= 200 && status < 300 {
			return fail("invalid_resource_metadata", "protected resource metadata %s is invalid for resource %s: %v", candidate, resource, err)
		}
		if i+1 < len(candidates) {
			continue
		}
		if metadata != "" || len(candidates) == 1 && challengeFound {
			return fail("resource_metadata_not_found", "protected resource metadata %s is unavailable (%s)", candidate, statusText(status, err))
		}
		return fail("resource_metadata_not_found", "MCP endpoint %s returned no Bearer challenge with resource_metadata and no well-known protected resource metadata is available; declare oauth.resourceMetadataUrl if the server publishes it elsewhere", destination)
	}
	if result.prm == nil {
		return fail("resource_metadata_not_found", "MCP authorization discovery found no protected resource metadata for %s", destination)
	}
	if len(result.prm.AuthorizationServers) == 0 {
		return fail("authorization_server_missing", "protected resource metadata %s lists no authorization_servers", result.metadataURL)
	}
	addKnown(result.prm.AuthorizationServers...)
	addKnown(result.prm.Resource)
	issuer := result.prm.AuthorizationServers[0]
	if settings.Issuer != "" {
		if !slices.Contains(result.prm.AuthorizationServers, settings.Issuer) {
			return fail("issuer_not_advertised", "configured issuer %s is not among the authorization servers advertised by %s: %s", settings.Issuer, result.metadataURL, strings.Join(result.prm.AuthorizationServers, ", "))
		}
		issuer = settings.Issuer
	}
	if !secureURL(issuer) {
		return fail("insecure_authorization_server", "authorization server %q must use HTTPS (HTTP only for localhost)", issuer)
	}
	asm, err := sdkauth.GetAuthServerMetadata(ctx, issuer, &recording)
	if err != nil && strings.Contains(err.Error(), "PKCE") {
		return fail("pkce_s256_unsupported", "authorization server %s does not advertise PKCE S256 in code_challenge_methods_supported", issuer)
	}
	if err != nil || asm == nil {
		detail := "no OAuth or OpenID metadata document was found"
		if err != nil {
			detail = err.Error()
		}
		return fail("authorization_server_metadata_not_found", "authorization server %s metadata discovery failed: %s", issuer, detail)
	}
	result.asm = asm
	addKnown(asm.Issuer, asm.AuthorizationEndpoint, asm.TokenEndpoint, asm.RegistrationEndpoint)
	for name, endpoint := range map[string]string{"authorization_endpoint": asm.AuthorizationEndpoint, "token_endpoint": asm.TokenEndpoint} {
		if !secureURL(endpoint) {
			return fail("insecure_authorization_server", "authorization server %s %s %q must use HTTPS (HTTP only for localhost)", issuer, name, endpoint)
		}
	}
	switch {
	case settings.ClientID != "":
		result.registration = "configured"
	case asm.ClientIDMetadataDocumentSupported && settings.ClientMetadataURL != "":
		result.registration = "client_metadata"
	case secureURL(asm.RegistrationEndpoint):
		result.registration = "dynamic"
	default:
		return fail("client_registration_unavailable", "authorization server %s supports neither dynamic client registration nor a configured Client ID Metadata Document; configure a pre-registered client through oauthClient or oauth.clientId", issuer)
	}
	if !slices.Contains(asm.CodeChallengeMethodsSupported, "S256") {
		return fail("pkce_s256_unsupported", "authorization server %s does not advertise PKCE S256 in code_challenge_methods_supported", issuer)
	}
	result.steps = recorder.snapshot()
	return result, nil
}

func statusText(status int, err error) string {
	if status != 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	return err.Error()
}

// probeChallenge sends an unauthenticated initialize. A server that does not
// require authorization may open a session; it is closed immediately.
func probeChallenge(ctx context.Context, client *http.Client, recorder *discoveryRecorder, destination string, headers map[string]string) (metadata string, scopes []string, err error) {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"agent-platform-authorization-discovery","version":"1.0.0"}}}`, discoveryProtocolVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination, strings.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	// Only the status and headers matter. An SSE response may never end, so the
	// body is closed unread instead of drained.
	resp.Body.Close()
	if session := resp.Header.Get("Mcp-Session-Id"); resp.StatusCode < 300 && session != "" {
		closeProbeSession(ctx, client, destination, headers, session)
	}
	challenges, parseErr := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if parseErr != nil {
		recorder.annotateLast(http.MethodPost, "invalid WWW-Authenticate header")
		return "", nil, nil
	}
	bearer := false
	for _, c := range challenges {
		if !strings.EqualFold(c.Scheme, "Bearer") {
			continue
		}
		bearer = true
		if scope := c.Params["scope"]; scope != "" && scopes == nil {
			scopes = strings.Fields(scope)
		}
		if c.Params["resource_metadata"] != "" {
			recorder.annotateLast(http.MethodPost, "Bearer challenge with resource_metadata")
			return c.Params["resource_metadata"], scopes, nil
		}
	}
	if bearer {
		recorder.annotateLast(http.MethodPost, "Bearer challenge without resource_metadata")
	} else {
		recorder.annotateLast(http.MethodPost, "no Bearer challenge")
	}
	return "", scopes, nil
}

func closeProbeSession(ctx context.Context, client *http.Client, destination string, headers map[string]string, session string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, destination, bytes.NewReader(nil))
	if err != nil {
		return
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Mcp-Session-Id", session)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

// wellKnownResourceMetadata lists RFC 9728 locations: path insertion first,
// then the host root.
func wellKnownResourceMetadata(destination string) []string {
	u, err := url.Parse(destination)
	if err != nil {
		return nil
	}
	u.RawQuery, u.Fragment, u.RawPath = "", "", ""
	path := u.Path
	u.Path = "/.well-known/oauth-protected-resource"
	root := u.String()
	if path == "" || path == "/" {
		return []string{root}
	}
	u.Path = "/.well-known/oauth-protected-resource" + path
	return []string{u.String(), root}
}

// componentURLs lists the package's MCP endpoints for diagnostic redaction.
func componentURLs(pkg connector.Package) []string {
	var urls []string
	for _, component := range pkg.MCP {
		if value, ok := component["url"].(string); ok {
			urls = append(urls, value)
		}
	}
	return urls
}

// probeHeaders are the component's constant headers. Templated values hold
// credentials and are never sent before authorization.
func probeHeaders(component map[string]any) map[string]string {
	headers := map[string]string{}
	values, _ := component["headers"].(map[string]any)
	for key, value := range values {
		text, ok := value.(string)
		if !ok || strings.Contains(text, "${") || strings.EqualFold(key, "Authorization") {
			continue
		}
		headers[key] = text
	}
	return headers
}

// DiscoverAuthorization checks, without signing in or registering a client,
// whether MCP authorization can start for one component of an authMode mcp
// package. The report lists every request so failures can be diagnosed.
func DiscoverAuthorization(ctx context.Context, client *http.Client, pkg connector.Package, component string) DiscoveryReport {
	report := DiscoveryReport{Status: "failed", Steps: []DiscoveryStep{}}
	if pkg.AuthMode != connector.AuthMCP {
		report.Code, report.Message = "not_mcp_authorization", fmt.Sprintf("authorization discovery applies to authMode mcp, but authMode is %s", pkg.AuthMode)
		return report
	}
	selected, err := OAuthComponent(pkg, component)
	if err != nil {
		report.Code, report.Message = "invalid_component", redactKnown(err.Error(), componentURLs(pkg)...)
		return report
	}
	for name := range selected.MCP {
		report.ComponentID = name
	}
	resource, settings, err := mcpAuthorizationSettings(selected)
	if err != nil {
		report.Code, report.Message = "invalid_configuration", redactKnown(err.Error(), componentURLs(pkg)...)
		return report
	}
	report.Resource = redactURLString(resource)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	found, err := discoverAuthorization(ctx, client, resource, oauthDestination(selected), probeHeaders(selected.MCP[report.ComponentID]), settings)
	if err != nil {
		if discoveryErr, ok := err.(*DiscoveryError); ok {
			report.Code, report.Message, report.Steps = discoveryErr.Code, discoveryErr.Message, discoveryErr.Steps
		} else {
			report.Code, report.Message = "discovery_failed", redactKnown(err.Error(), componentURLs(pkg)...)
		}
		return report
	}
	report.Status, report.Message, report.Steps = "passed", "MCP authorization discovery passed; sign in to authorize", found.steps
	report.ResourceMetadataURL, report.Issuer, report.ClientRegistration = redactURLString(found.metadataURL), redactURLString(found.asm.Issuer), found.registration
	report.Scopes = authorizationScopes(selected, resource, settings, found)
	return report
}
