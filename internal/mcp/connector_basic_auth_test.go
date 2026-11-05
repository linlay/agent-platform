package mcp

import (
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenHTTPBasicCredentialsAtRequestBoundary(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	endpoint := "https://example.test/mcp"
	for file, value := range map[string]any{
		"connector.json":    map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "authMode": "token"},
		"token-schema.json": map[string]any{"fields": []map[string]any{{"key": "USERNAME", "required": true}, {"key": "PASSWORD", "required": true}}},
		"mcp.json":          map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": endpoint, "headers": map[string]any{"Authorization": "Basic ${USERNAME}:${PASSWORD}"}}}},
	} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, file), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	manager := connectorauth.New(context.Background(), sources, nil).WithCredentialValidator(func(context.Context, connector.Package, map[string]string) error { return nil })
	username, password := "用户", "secret:with:colons"
	if _, err := manager.SetToken(t.Context(), "demo", map[string]string{"USERNAME": username, "PASSWORD": password}); err != nil {
		t.Fatal(err)
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := connectorServer(pkg, "main")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		user, pass, ok := req.BasicAuth()
		if !ok || user != username || pass != password {
			t.Fatal("Basic credentials were not encoded correctly")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: req}, nil
	})
	client := &Client{httpClient: &http.Client{Transport: base}}
	transport := client.httpClientForServer(definition).Transport
	req, _ := http.NewRequest(http.MethodPost, endpoint, nil)
	for _, next := range []string{password, "rotated"} {
		password = next
		if _, err := manager.SetToken(t.Context(), "demo", map[string]string{"USERNAME": username, "PASSWORD": password}); err != nil {
			t.Fatal(err)
		}
		response, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if req.Header.Get("Authorization") != "" {
			t.Fatal("credentials modified public request")
		}
	}
	if _, err := manager.SetToken(t.Context(), "demo", map[string]string{"USERNAME": "bad:name", "PASSWORD": password}); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("accepted colon in username")
	}
	if calls != 2 {
		t.Fatal("invalid username sent upstream")
	}
	if err := manager.Logout(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("request survived logout")
	}
}
