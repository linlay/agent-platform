package connectorauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/httpclient"
)

// Revocation is attempted only for endpoints explicitly advertised by the
// authorization server. A missing endpoint remains local-only logout; no URL
// is inferred from the issuer, authorization endpoint or token endpoint.
func revokeOAuthCredentials(ctx context.Context, root, id string, client *http.Client) (string, error) {
	credentials, err := allOAuthCredentials(root, id)
	if err != nil {
		return "failed", err
	}
	if client == nil {
		client = httpclient.NewClient(15 * time.Second)
	}
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	status := "succeeded"
	if len(credentials) == 0 {
		status = "unsupported"
	}
	var failure error
	for _, credential := range credentials {
		supported, err := revokeOAuthCredential(ctx, credential, &safeClient)
		if err != nil {
			failure = fmt.Errorf("OAuth remote revocation failed")
			status = "failed"
		} else if !supported && status != "failed" {
			status = "unsupported"
		}
	}
	return status, failure
}

func revokeOAuthCredential(ctx context.Context, c oauthCredential, client *http.Client) (bool, error) {
	if c.Token == nil || c.RevocationURL == "" {
		return false, nil
	}
	if !secureURL(c.RevocationURL) {
		return false, fmt.Errorf("invalid advertised OAuth revocation endpoint")
	}
	method := "none"
	if c.Config.ClientSecret != "" {
		if len(c.RevocationAuthMethods) == 0 || slices.Contains(c.RevocationAuthMethods, "client_secret_basic") {
			method = "client_secret_basic"
		} else if slices.Contains(c.RevocationAuthMethods, "client_secret_post") {
			method = "client_secret_post"
		} else {
			return false, nil
		}
	} else if len(c.RevocationAuthMethods) > 0 && !slices.Contains(c.RevocationAuthMethods, "none") {
		return false, nil
	}
	for _, entry := range [][2]string{{"refresh_token", c.Token.RefreshToken}, {"access_token", c.Token.AccessToken}} {
		if entry[1] == "" {
			continue
		}
		values := url.Values{"token": {entry[1]}, "token_type_hint": {entry[0]}}
		if method != "client_secret_basic" {
			values.Set("client_id", c.Config.ClientID)
		}
		if method == "client_secret_post" {
			values.Set("client_secret", c.Config.ClientSecret)
		}
		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.RevocationURL, strings.NewReader(values.Encode()))
		if err != nil {
			cancel()
			return true, fmt.Errorf("OAuth revocation request is invalid")
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if method == "client_secret_basic" {
			req.SetBasicAuth(url.QueryEscape(c.Config.ClientID), url.QueryEscape(c.Config.ClientSecret))
		}
		response, err := client.Do(req)
		if err != nil {
			cancel()
			return true, fmt.Errorf("OAuth revocation request failed")
		}
		io.Copy(io.Discard, io.LimitReader(response.Body, 8192))
		response.Body.Close()
		cancel()
		if response.StatusCode != http.StatusOK {
			return true, fmt.Errorf("OAuth revocation rejected")
		}
	}
	return true, nil
}

// Revocation failure never prevents local unlinking. Keeping the refresh lock
// through deletion also prevents an in-flight refresh resurrecting a token.
func (m *Manager) revokeAndClearOAuth(ctx context.Context, pkg connector.Package) (string, error, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Second)
	defer cancel()
	unlock, err := lockCredentials(cleanupCtx, pkg.CredentialRoot(), pkg.ID)
	if err != nil {
		return "failed", nil, err
	}
	defer unlock()
	if err := changeAuthState(pkg.CredentialRoot(), pkg.ID, true); err != nil {
		return "failed", nil, err
	}
	status, revokeErr := revokeOAuthCredentials(ctx, pkg.CredentialRoot(), pkg.ID, m.client)
	if err := deleteOAuthCredentials(pkg.CredentialRoot(), pkg.ID); err != nil {
		return "failed", nil, err
	}
	return status, revokeErr, nil
}

func allOAuthCredentials(root, id string) ([]oauthCredential, error) {
	var result []oauthCredential
	legacy, err := credentialPath(root, id)
	if err != nil {
		return nil, err
	}
	var c oauthCredential
	if err := connector.ReadJSON(legacy, &c); err == nil {
		result = append(result, c)
		for _, grant := range c.Grants {
			result = append(result, grant)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	dir, err := StateDir(root, id)
	if err != nil {
		return nil, err
	}
	resourceDir := filepath.Join(dir, "oauth-resources")
	if info, err := os.Lstat(resourceDir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("invalid resource credential directory")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	files, err := os.ReadDir(resourceDir)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		info, err := file.Info()
		if err != nil || !info.Mode().IsRegular() || file.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("invalid resource credential file")
		}
		var c oauthCredential
		if err := connector.ReadJSON(filepath.Join(resourceDir, file.Name()), &c); err != nil {
			return nil, err
		}
		expected, err := resourceCredentialPath(root, id, c.Resource, c.Destination)
		if err != nil || filepath.Base(expected) != file.Name() {
			return nil, fmt.Errorf("invalid resource credential binding")
		}
		result = append(result, c)
	}
	return result, nil
}
