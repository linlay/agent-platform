package connectorauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"agent-platform/internal/connector"
)

// OAuthClientInfo is deployment-owned registration, never exported in status.
type OAuthClientInfo struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"`
	RedirectURI  string `json:"redirectUri,omitempty"`
	AuthMethod   string `json:"tokenEndpointAuthMethod,omitempty"`
}

func clientInfoPath(pkg connector.Package) (string, error) {
	dir, err := StateDir(pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "client-"+grantKey("", oauthDestination(pkg))+".json"), nil
}

func (m *Manager) SetOAuthClient(ctx context.Context, id, component string, info OAuthClientInfo) (Session, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Session{}, err
	}
	if pkg.AuthMode != connector.AuthMCP {
		return Session{}, fmt.Errorf("OAuth client configuration requires authMode mcp, but connector %s uses %s", id, pkg.AuthMode)
	}
	pkg, err = OAuthComponent(pkg, component)
	if err != nil {
		return Session{}, err
	}
	switch {
	case !secureURL(info.Issuer):
		return Session{}, fmt.Errorf("oauthClient.issuer must be an HTTPS URL (HTTP only for localhost)")
	case strings.TrimSpace(info.ClientID) == "":
		return Session{}, fmt.Errorf("oauthClient.clientId is required")
	case strings.ContainsAny(info.ClientID+info.ClientSecret, "\r\n\x00"):
		return Session{}, fmt.Errorf("oauthClient.clientId and clientSecret must be single-line values")
	case len(info.ClientID)+len(info.ClientSecret) > 64*1024:
		return Session{}, fmt.Errorf("oauthClient.clientId and clientSecret together must not exceed 64 KiB")
	}
	if info.RedirectURI != "" {
		if _, _, err := callbackAddress(info.RedirectURI); err != nil {
			return Session{}, err
		}
	}
	if info.AuthMethod != "" && info.AuthMethod != "none" && info.AuthMethod != "client_secret_basic" && info.AuthMethod != "client_secret_post" {
		return Session{}, fmt.Errorf("oauthClient.tokenEndpointAuthMethod %q must be none, client_secret_basic or client_secret_post", info.AuthMethod)
	}
	if info.AuthMethod == "none" && info.ClientSecret != "" {
		return Session{}, fmt.Errorf("oauthClient.clientSecret is not allowed with tokenEndpointAuthMethod none")
	}
	path, err := clientInfoPath(pkg)
	if err != nil {
		return Session{}, err
	}
	unlock, err := lockCredentials(ctx, pkg.PersistentRoot(), id)
	if err != nil {
		return Session{}, err
	}
	defer unlock()
	if err := changeAuthState(pkg.PersistentRoot(), id, true); err != nil {
		return Session{}, err
	}
	if err := savePrivateJSON(path, info); err != nil {
		return Session{}, err
	}
	return Session{ConnectorID: id, ComponentID: component, Status: "unauthorized", Message: "OAuth client configured; start authorization"}, nil
}

func readClientInfo(pkg connector.Package) (OAuthClientInfo, error) {
	path, err := clientInfoPath(pkg)
	if err != nil {
		return OAuthClientInfo{}, err
	}
	var info OAuthClientInfo
	err = readPrivateJSON(path, &info)
	if os.IsNotExist(err) {
		err = nil
	}
	return info, err
}

func saveRegisteredClient(ctx context.Context, pkg connector.Package, info OAuthClientInfo, generation string) error {
	unlock, err := lockCredentials(ctx, pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := readAuthState(pkg.PersistentRoot(), pkg.ID)
	if err != nil {
		return err
	}
	if state.Generation != generation || ctx.Err() != nil {
		return fmt.Errorf("authorization was canceled or signed out")
	}
	path, err := clientInfoPath(pkg)
	if err != nil {
		return err
	}
	return savePrivateJSON(path, info)
}

func validateClientMetadata(ctx context.Context, client *http.Client, raw, redirect string) error {
	// The document URL is connector configuration and may carry a query.
	shown := redactURLString(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Path == "" || u.User != nil || u.Fragment != "" || redirect == "" {
		return fmt.Errorf("oauth.clientMetadataUrl %q must be an HTTPS document URL and oauth.redirectUri must be configured", shown)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("client metadata document %s is unavailable: %s", shown, redactKnown(err.Error(), raw))
	}
	defer resp.Body.Close()
	var doc struct {
		ClientID     string   `json:"client_id"`
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("client metadata document %s returned HTTP %d", shown, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return fmt.Errorf("client metadata document %s is not valid JSON: %v", shown, err)
	}
	switch {
	case doc.ClientID != raw:
		return fmt.Errorf("client metadata document %s declares client_id %q; it must equal the document URL", shown, redactURLString(doc.ClientID))
	case doc.ClientName == "":
		return fmt.Errorf("client metadata document %s requires client_name", shown)
	case !slices.Contains(doc.RedirectURIs, redirect):
		return fmt.Errorf("client metadata document %s does not list redirect_uri %s", shown, redirect)
	}
	return nil
}
