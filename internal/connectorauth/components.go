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
	// TODO(connector-compat): 下个版本移除旧逐组件 OAuth 配置和 CLI grant 选择分支。
	if settings, grant := pkg.LegacyOAuthBinding(component); grant != "" {
		if component != "cli" || pkg.MCP[grant] == nil {
			return pkg, fmt.Errorf("CLI grant references missing MCP component")
		}
		return OAuthComponent(pkg, grant)
	} else if len(settings) != 0 {
		pkg.OAuth = settings
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
