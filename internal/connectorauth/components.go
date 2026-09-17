package connectorauth

import (
	"fmt"

	"agent-platform/internal/connector"
)

// OAuthComponent selects a grant without changing package identity or files.
func OAuthComponent(pkg connector.Package, component string) (connector.Package, error) {
	if component == "" {
		if len(pkg.MCP) > 1 {
			return pkg, fmt.Errorf("select an MCP component for authorization")
		}
		for name := range pkg.MCP {
			component = name
		}
		if component == "" {
			component = "cli"
		}
	}
	target := "mcp:" + component
	if component == "cli" {
		target = "cli"
	}
	binding := pkg.AuthBindings[target]
	if binding.Grant != "" {
		if target != "cli" {
			return pkg, fmt.Errorf("only CLI may consume another component grant")
		}
		if _, ok := pkg.MCP[binding.Grant]; !ok {
			return pkg, fmt.Errorf("CLI grant references missing MCP component")
		}
		return OAuthComponent(pkg, binding.Grant)
	}
	if len(binding.OAuth) > 0 {
		pkg.OAuth = binding.OAuth
	}
	if component == "cli" {
		if pkg.CLI == nil {
			return pkg, fmt.Errorf("CLI component not found")
		}
		pkg.MCP = nil
	} else {
		value, ok := pkg.MCP[component]
		if !ok {
			return pkg, fmt.Errorf("MCP component not found")
		}
		pkg.MCP = map[string]map[string]any{component: value}
	}
	return pkg, nil
}
