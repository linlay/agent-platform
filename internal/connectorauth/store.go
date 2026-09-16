// Package connectorauth owns deployment-scoped connector login sessions and
// credentials. It does not depend on HTTP handlers, Agent execution or MCP tools.
package connectorauth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/httpclient"
	"golang.org/x/oauth2"
)

// A file lock also serializes the standalone login process with the runtime.
// In particular, a refresh must not recreate credentials after logout returns.
func lockCredentials(ctx context.Context, root, id string) (func(), error) {
	dir, err := StateDir(root, id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "oauth.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid credential lock file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		locked, err := tryCredentialLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if locked {
			return func() { f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

type oauthCredential struct {
	Generation     string                     `json:"generation,omitempty"`
	MCP            bool                       `json:"mcp,omitempty"`
	RequiredScopes []string                   `json:"requiredScopes,omitempty"`
	RequiresLogin  bool                       `json:"requiresLogin,omitempty"`
	Grants         map[string]oauthCredential `json:"grants,omitempty"`
	Resource       string                     `json:"resource"`
	Destination    string                     `json:"destination,omitempty"`
	Config         oauth2.Config              `json:"config"`
	Token          *oauth2.Token              `json:"token"`
}

// StateDir is outside the installed, read-only package. Credentials are never
// part of connector.json, ZIP exports, prompts, or catalog response objects.
func StateDir(root, id string) (string, error) {
	return connector.StateDir(root, id)
}

func credentialPath(root, id string) (string, error) {
	dir, err := StateDir(root, id)
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "oauth.json")
	if info, err := os.Lstat(p); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return "", fmt.Errorf("invalid credential file")
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return p, nil
}

func readCredential(root, id string) (oauthCredential, error) {
	p, err := credentialPath(root, id)
	if err != nil {
		return oauthCredential{}, err
	}
	var c oauthCredential
	err = connector.ReadJSON(p, &c)
	return c, err
}

func saveCredential(root, id string, c oauthCredential) error {
	p, err := credentialPath(root, id)
	if err != nil {
		return err
	}
	previous, err := readCredential(root, id)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if previous.Grants == nil {
		previous.Grants = map[string]oauthCredential{}
	}
	if previous.Token != nil && !credentialMatches(previous, c.Resource, []string{c.Destination}) {
		old := previous
		old.Grants = nil
		previous.Grants[grantKey(old.Resource, old.Destination)] = old
	}
	delete(previous.Grants, grantKey(c.Resource, c.Destination))
	c.Grants = previous.Grants
	return savePrivateJSON(p, c)
}

func grantKey(resource, destination string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(resource+"\x00"+destination)))
}

func selectCredential(c oauthCredential, resource string, destinations []string) (oauthCredential, bool) {
	if credentialMatches(c, resource, destinations) {
		return c, true
	}
	for _, grant := range c.Grants {
		if credentialMatches(grant, resource, destinations) {
			return grant, true
		}
	}
	return oauthCredential{}, false
}

func savePrivateJSON(p string, value any) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".oauth-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

func readPrivateJSON(p string, value any) error {
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("invalid credential file")
	}
	return connector.ReadJSON(p, value)
}

func CredentialReady(root, id, resource string, destinations ...string) bool {
	// Atomic file replacement allows a local snapshot without waiting for a
	// token refresh. Catalog reload must never wait on authentication network I/O.
	c, err := readCredential(root, id)
	c, matches := selectCredential(c, resource, destinations)
	s, stateErr := readAuthState(root, id)
	return err == nil && stateErr == nil && c.Generation == s.Generation && !c.RequiresLogin && matches && c.Token != nil && (c.Token.Valid() || c.Token.RefreshToken != "")
}

func credentialMatches(c oauthCredential, resource string, destinations []string) bool {
	if c.Resource != resource {
		return false
	}
	destination := c.Destination
	if destination == "" {
		destination = c.Resource
	} // Credentials written before audience/destination separation.
	return len(destinations) == 0 || destination == destinations[0]
}

// AccessToken re-reads storage for every request, so logout and rotated refresh
// tokens also affect existing MCP sessions. Refresh is serialized across clients.
func AccessToken(ctx context.Context, root, id, resource string, client *http.Client, destinations ...string) (string, error) {
	unlock, err := lockCredentials(ctx, root, id)
	if err != nil {
		return "", err
	}
	defer unlock()
	c, err := readCredential(root, id)
	c, matches := selectCredential(c, resource, destinations)
	s, stateErr := readAuthState(root, id)
	if err != nil || stateErr != nil || c.Generation != s.Generation || c.RequiresLogin || !matches || c.Token == nil {
		return "", fmt.Errorf("connector requires login")
	}
	if c.Token.Valid() {
		return c.Token.AccessToken, nil
	}
	if c.Token.RefreshToken == "" {
		return "", fmt.Errorf("connector login expired")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if client == nil {
		client = httpclient.NewClient(30 * time.Second)
	}
	refreshResource := ""
	if c.MCP {
		refreshResource = c.Resource
	}
	client = tokenHTTPClient(client, c.Config.Endpoint.TokenURL, refreshResource)
	ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	token, err := c.Config.TokenSource(ctx, c.Token).Token()
	if err != nil {
		var rejected *oauth2.RetrieveError
		if errors.As(err, &rejected) && (rejected.ErrorCode == "invalid_grant" || rejected.ErrorCode == "invalid_client") {
			c.RequiresLogin = true
			if persistErr := saveCredential(root, id, c); persistErr != nil {
				return "", fmt.Errorf("persist authentication failure")
			}
		}
		return "", fmt.Errorf("connector token refresh failed; sign in again")
	}
	if token.RefreshToken == "" {
		token.RefreshToken = c.Token.RefreshToken
	}
	c.Token = token
	if err := saveCredential(root, id, c); err != nil {
		return "", fmt.Errorf("persist refreshed connector credential: %w", err)
	}
	return token.AccessToken, nil
}

// AuthorizingTransport only sends a credential to the exact configured resource.
// An HTTP redirect cannot carry it to another host or path.
type AuthorizingTransport struct {
	Base               http.RoundTripper
	Client             *http.Client
	Root, ID, Resource string
	CredentialResource string
}

func (t AuthorizingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	want, err := url.Parse(t.Resource)
	if err != nil || req.URL.Scheme != want.Scheme || req.URL.Host != want.Host || req.URL.EscapedPath() != want.EscapedPath() || req.URL.RawQuery != want.RawQuery {
		return nil, fmt.Errorf("connector credential destination mismatch")
	}
	audience := t.CredentialResource
	if audience == "" {
		audience = t.Resource
	}
	token, err := AccessToken(req.Context(), t.Root, t.ID, audience, t.Client, t.Resource)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+token)
	response, err := t.Base.RoundTrip(clone)
	if err != nil {
		return nil, err
	}
	retry, rejectErr := t.rejected(req.Context(), response, token, false)
	if rejectErr != nil {
		response.Body.Close()
		return nil, fmt.Errorf("connector authentication update failed")
	}
	if !retry || req.Body != nil && req.GetBody == nil {
		return response, nil
	}
	response.Body.Close()
	newToken, err := AccessToken(req.Context(), t.Root, t.ID, audience, t.Client, t.Resource)
	if err != nil {
		return nil, err
	}
	if req.Body != nil {
		clone.Body, err = req.GetBody()
		if err != nil {
			return nil, err
		}
	}
	clone.Header.Set("Authorization", "Bearer "+newToken)
	response, err = t.Base.RoundTrip(clone)
	if err == nil {
		_, _ = t.rejected(req.Context(), response, newToken, true)
	}
	return response, err
}
