package mcp

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"agent-platform/internal/connector"
)

func validateTokenQuery(pkg connector.Package, u *url.URL) error {
	if !strings.Contains(u.String(), "${") {
		return nil
	}
	if pkg.AuthMode != connector.AuthToken || strings.Contains(u.Scheme+u.Host+u.Path, "${") {
		return fmt.Errorf("credential URL templates require token mode and query values only")
	}
	fields, err := connector.TokenFields(pkg.Manifest)
	if err != nil {
		return err
	}
	declared := map[string]string{}
	for _, f := range fields {
		declared[f.Key] = "placeholder"
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return fmt.Errorf("invalid connector URL query")
	}
	for key, values := range query {
		if strings.Contains(key, "${") {
			return fmt.Errorf("credential templates cannot appear in query names")
		}
		for _, value := range values {
			if _, err := resolveConnectorCredential(pkg, value, declared); err != nil {
				return fmt.Errorf("query references an undeclared credential")
			}
		}
	}
	return nil
}

// Keep the source URL in registries, logs and SDK errors. Credentials are read
// and query-escaped at the final request boundary, never written into BaseURL.
type tokenQueryTransport struct {
	base               http.RoundTripper
	root, id, resource string
	headers            map[string]string
}

func (t tokenQueryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	want, err := url.Parse(t.resource)
	if err != nil || req.URL.Scheme != want.Scheme || req.URL.Host != want.Host || req.URL.EscapedPath() != want.EscapedPath() || req.URL.Query().Encode() != want.Query().Encode() {
		return nil, fmt.Errorf("connector credential destination mismatch")
	}
	path, err := connector.CredentialsPath(t.root, t.id)
	if err != nil {
		return nil, fmt.Errorf("connector requires credentials")
	}
	var credentials map[string]string
	if err = connector.ReadJSON(path, &credentials); err != nil {
		return nil, fmt.Errorf("connector requires credentials")
	}
	query := want.Query()
	for key, values := range query {
		for i, value := range values {
			values[i], err = resolveConnectorCredential(connector.Package{Manifest: connector.Manifest{ID: t.id}}, value, credentials)
			if err != nil {
				return nil, fmt.Errorf("connector requires credentials")
			}
		}
		query[key] = values
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	for key, template := range t.headers {
		value, err := resolveConnectorCredential(connector.Package{Manifest: connector.Manifest{ID: t.id}}, template, credentials)
		if err != nil || strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("connector requires valid credentials")
		}
		clone.Header.Set(key, value)
	}
	target := *req.URL
	target.RawQuery = query.Encode()
	clone.URL = &target
	response, err := t.base.RoundTrip(clone)
	if err != nil {
		return nil, fmt.Errorf("connector authenticated HTTP request failed")
	}
	response.Request = req
	return response, nil
}
