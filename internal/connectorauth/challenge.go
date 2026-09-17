package connectorauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Only an explicit authentication rejection permits replay, never a network
// error or a response whose tool execution status is unknown.
func (t AuthorizingTransport) rejected(ctx context.Context, response *http.Response, usedToken string, final bool) (bool, error) {
	if response.StatusCode != 401 && response.StatusCode != 403 {
		return false, nil
	}
	challenges, err := oauthex.ParseWWWAuthenticate(response.Header.Values("WWW-Authenticate"))
	if err != nil {
		return false, nil
	}
	var scopes []string
	insufficient := false
	for _, c := range challenges {
		if strings.EqualFold(c.Scheme, "Bearer") && c.Params["error"] == "insufficient_scope" {
			insufficient = true
			scopes = strings.Fields(c.Params["scope"])
		}
	}
	if response.StatusCode == 403 && !insufficient {
		return false, nil
	}
	unlock, err := lockCredentials(ctx, t.Root, t.ID)
	if err != nil {
		return false, err
	}
	defer unlock()
	resource := t.CredentialResource
	if resource == "" {
		resource = t.Resource
	}
	c, err := readCredential(t.Root, t.ID)
	if err != nil {
		return false, err
	}
	c, ok := selectCredential(c, resource, []string{t.Resource})
	if !ok || c.Token == nil {
		return false, nil
	}
	if c.Token.AccessToken != usedToken {
		return true, nil
	}
	if insufficient {
		c.RequiredScopes = scopes
		c.RequiresLogin = true
	} else {
		c.Token.Expiry = time.Now().Add(-time.Minute)
		c.RequiresLogin = final || c.Token.RefreshToken == ""
	}
	if err := saveCredential(t.Root, t.ID, c); err != nil {
		return false, err
	}
	return !c.RequiresLogin, nil
}

// resourceTransport adds the audience to MCP refresh requests; it never changes
// the general OAuth flow or follows a token endpoint redirect with credentials.
type resourceTransport struct {
	base               http.RoundTripper
	endpoint, resource string
}

func (t resourceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != t.endpoint || req.Method != http.MethodPost {
		return nil, fmt.Errorf("OAuth token destination mismatch")
	}
	data, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	form, err := url.ParseQuery(string(data))
	if err != nil {
		return nil, err
	}
	if t.resource != "" {
		form.Set("resource", t.resource)
	}
	encoded := form.Encode()
	copy := req.Clone(req.Context())
	copy.Body = io.NopCloser(strings.NewReader(encoded))
	copy.ContentLength = int64(len(encoded))
	return t.base.RoundTrip(copy)
}

func tokenHTTPClient(client *http.Client, endpoint, resource string) *http.Client {
	copy := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copy.Transport = resourceTransport{base: base, endpoint: endpoint, resource: resource}
	return &copy
}
