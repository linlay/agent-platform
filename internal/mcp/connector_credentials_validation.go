package mcp

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

// ValidateOwnerCredentials uses an isolated client and registry. The caller has
// already staged these values under a temporary private owner; the live user's
// credentials, bound/enabled preferences, sessions and tool catalog are untouched.
func (c *Client) ValidateOwnerCredentials(ctx context.Context, pkg connector.Package, values map[string]string) error {
	if pkg.Owner == "" || pkg.AuthMode != connector.AuthToken || len(pkg.MCP) == 0 {
		return fmt.Errorf("invalid connector credential validation request")
	}
	stored, ready, err := connectorauth.TokenValues(pkg)
	if err != nil || !ready || len(stored) != len(values) {
		return fmt.Errorf("candidate credentials are unavailable")
	}
	for key, value := range values {
		if stored[key] != value {
			return fmt.Errorf("candidate credentials changed")
		}
	}
	servers := map[string]ServerDefinition{}
	keys := make([]string, 0, len(pkg.MCP))
	for name := range pkg.MCP {
		server, err := connectorServer(pkg, name)
		if err != nil {
			return fmt.Errorf("connector component validation setup failed")
		}
		servers[server.Key] = server
		keys = append(keys, server.Key)
	}
	sort.Strings(keys)
	client := *c.httpClient
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	probe := &credentialProbeTransport{base: transport}
	client.Transport = probe
	validation := NewClientWithGate(&Registry{servers: servers}, &client, NewAvailabilityGate())
	validation.owner = pkg.Owner
	defer validation.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, key := range keys {
		if err := validation.Initialize(ctx, key); err != nil {
			if probe.rejected.Load() {
				return connectorauth.ErrTokenRejected
			}
			return fmt.Errorf("connector initialization failed during credential check")
		}
		if _, err := validation.ListTools(ctx, key); err != nil {
			if probe.rejected.Load() {
				return connectorauth.ErrTokenRejected
			}
			return fmt.Errorf("connector tool discovery failed during credential check")
		}
	}
	return nil
}

type credentialProbeTransport struct {
	base     http.RoundTripper
	rejected atomic.Bool
}

func (t *credentialProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if response != nil && response.StatusCode == http.StatusUnauthorized {
		t.rejected.Store(true)
	}
	return response, err
}
