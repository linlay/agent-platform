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
	if pkg.AuthMode != connector.AuthMCP && pkg.AuthMode != connector.AuthOAuth {
		return Session{}, fmt.Errorf("OAuth client configuration requires oauth or mcp mode")
	}
	pkg, err = OAuthComponent(pkg, component)
	if err != nil {
		return Session{}, err
	}
	if !secureURL(info.Issuer) || strings.TrimSpace(info.ClientID) == "" || strings.ContainsAny(info.ClientID+info.ClientSecret, "\r\n\x00") || len(info.ClientID)+len(info.ClientSecret) > 64*1024 {
		return Session{}, fmt.Errorf("invalid OAuth client information")
	}
	if info.RedirectURI != "" {
		if _, _, err := callbackAddress(info.RedirectURI); err != nil {
			return Session{}, err
		}
	}
	if info.AuthMethod != "" && info.AuthMethod != "none" && info.AuthMethod != "client_secret_basic" && info.AuthMethod != "client_secret_post" {
		return Session{}, fmt.Errorf("unsupported client authentication method")
	}
	if info.AuthMethod == "none" && info.ClientSecret != "" {
		return Session{}, fmt.Errorf("public client cannot have a client secret")
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
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Path == "" || u.User != nil || u.Fragment != "" || redirect == "" {
		return fmt.Errorf("client metadata requires an HTTPS document URL and registered redirect_uri")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("client metadata unavailable")
	}
	defer resp.Body.Close()
	var doc struct {
		ClientID     string   `json:"client_id"`
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc) != nil || doc.ClientID != raw || doc.ClientName == "" || !slices.Contains(doc.RedirectURIs, redirect) {
		return fmt.Errorf("invalid client metadata or unregistered callback")
	}
	return nil
}
