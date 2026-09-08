package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func TestTokenQueryStaysOutOfRegistryAndReadsCurrentCredentials(t *testing.T) {
	sources := connector.Sources{ExternalRoot: t.TempDir()}
	dir := filepath.Join(sources.ExternalRoot, "demo")
	os.MkdirAll(dir, 0755)
	endpoint := "https://example.test/mcp?key=${API_KEY}&format=0"
	for file, value := range map[string]any{
		"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "mcp", "auth_mode": "token", "token_schema": map[string]any{"fields": []map[string]any{{"key": "API_KEY", "required": true}}}},
		"mcp.json":       map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": endpoint, "headers": map[string]any{"Authorization": "Bearer ${API_KEY}"}, "staticHeaders": map[string]any{"X-Trace": "public"}}}},
	} {
		b, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, file), b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	manager := connectorauth.New(ctx, sources, nil)
	current := "first&other=not-another-param+#中文"
	if _, err := manager.SetToken(ctx, "demo", map[string]string{"API_KEY": current}); err != nil {
		t.Fatal(err)
	}
	pkg, err := sources.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := connectorServer(pkg, "main")
	if err != nil || !definition.ConnectorTokenQuery || definition.SetupError != "" || definition.ResolvedURL() != endpoint {
		t.Fatal("registry changed credential template", err)
	}
	fail := false
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer "+current || req.Header.Get("X-Trace") != "public" {
			t.Error("current header credential or static header missing")
		}
		if req.URL.Query().Get("key") != current || req.URL.Query().Get("format") != "0" || len(req.URL.Query()) != 2 {
			t.Error("token was not safely query encoded")
		}
		if fail {
			return nil, fmt.Errorf("request failed: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: req}, nil
	})
	client := &Client{httpClient: &http.Client{Transport: base}}
	transport := client.httpClientForServer(definition).Transport
	request, _ := http.NewRequest(http.MethodPost, endpoint, nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.Request.URL.String() != endpoint || request.URL.String() != endpoint {
		t.Fatal("credential escaped to public request")
	}
	current = "rotated"
	manager.SetToken(ctx, "demo", map[string]string{"API_KEY": current})
	response, err = transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	for _, bad := range []string{"https://other.test/mcp?key=${API_KEY}&format=0", "https://example.test/other?key=${API_KEY}&format=0", "https://example.test/mcp?key=${API_KEY}&format=1"} {
		req, _ := http.NewRequest(http.MethodPost, bad, nil)
		if _, err := transport.RoundTrip(req); err == nil {
			t.Fatal("credential destination escaped")
		}
	}
	fail = true
	if _, err := transport.RoundTrip(request); err == nil || strings.Contains(err.Error(), current) {
		t.Fatal("upstream error leaked credential", err)
	}
	if err := manager.Logout(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("old request survived logout")
	}
}
