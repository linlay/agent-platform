package mcp

import (
	"agent-platform/internal/connector"
	"fmt"
)

// NewOperationClient creates a single-component, short-lived session using the
// caller's isolated credential locator. It never borrows an Agent session.
func NewOperationClient(pkg connector.Package, component, toolName string) (*Client, string, error) {
	if pkg.AuthMode == connector.AuthOneID || pkg.MCP[component] == nil {
		return nil, "", fmt.Errorf("unsupported personal MCP component")
	}
	server, err := connectorServer(pkg, component)
	if err != nil {
		return nil, "", err
	}
	if !server.Enabled() || server.AuthSource == AuthSourceIdentityFile {
		return nil, "", fmt.Errorf("unsupported personal MCP authentication")
	}
	for _, disabled := range server.DisabledTools {
		if disabled == toolName {
			return nil, "", fmt.Errorf("MCP operation tool is disabled")
		}
	}
	server.IsolatedEnvironment = true
	registry := &Registry{servers: map[string]ServerDefinition{server.Key: server}, version: 1}
	return NewClientWithGate(registry, nil, nil), server.Key, nil
}
