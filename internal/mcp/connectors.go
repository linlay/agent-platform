package mcp

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/connector"
)

func loadConnectorServers(sources connector.Sources) (map[string]ServerDefinition, error) {
	packages, err := sources.LoadAll()
	if err != nil {
		return nil, err
	}
	servers := map[string]ServerDefinition{}
	for _, pkg := range packages {
		names := make([]string, 0, len(pkg.MCP))
		for name := range pkg.MCP {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			server, err := connectorServer(pkg, name)
			if err != nil {
				return nil, fmt.Errorf("connector %s MCP %s: %w", pkg.ID, name, err)
			}
			if !server.Enabled() {
				continue
			}
			if _, exists := servers[server.Key]; exists {
				return nil, fmt.Errorf("duplicate connector MCP server key %q", server.Key)
			}
			servers[server.Key] = server
		}
	}
	return servers, nil
}

// The platform block carries explicit deployment settings migrated from the
// former registry (tool aliases/overrides and the platform identity adapter).
// It is not a second source of MCP definitions.
func connectorServer(pkg connector.Package, name string) (ServerDefinition, error) {
	component := pkg.MCP[name]
	allowed := map[string]bool{}
	for _, key := range []string{"type", "url", "command", "args", "runtime", "timeout", "headers", "staticHeaders", "env", "staticEnv", "disabledTools", "platform"} {
		allowed[key] = true
	}
	for key := range component {
		if !allowed[key] {
			return ServerDefinition{}, fmt.Errorf("unknown MCP field %q", key)
		}
	}
	credentials := map[string]string{}
	credentialReady := false
	if pkg.AuthMode == "token" {
		err := connector.ReadJSON(filepath.Join(filepath.Dir(pkg.Dir), ".credentials", pkg.ID+".json"), &credentials)
		if err == nil {
			credentialReady = true
		} else if !os.IsNotExist(err) {
			return ServerDefinition{}, fmt.Errorf("connector credential store is invalid")
		}
	}
	tree := map[string]any{}
	if platform, ok := component["platform"].(map[string]any); ok {
		for key, value := range platform {
			switch key {
			case "enabled", "name", "workingDirectory", "toolPrefix", "authSource", "aliasMap", "connect-timeout", "startup-timeout", "retry", "tools":
				tree[key] = value
			default:
				return ServerDefinition{}, fmt.Errorf("unknown platform MCP setting %q", key)
			}
		}
	} else if _, exists := component["platform"]; exists {
		return ServerDefinition{}, fmt.Errorf("platform must be an object")
	}
	tree["serverKey"] = connector.ServerKey(pkg.ID, name)
	switch component["type"] {
	case "streamableHttp":
		if hasAnyKey(component, "command", "args", "env", "staticEnv", "runtime") {
			return ServerDefinition{}, fmt.Errorf("HTTP MCP cannot declare stdio fields")
		}
		rawURL, _ := component["url"].(string)
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
			return ServerDefinition{}, fmt.Errorf("MCP url must be an HTTP(S) URL without userinfo or fragment")
		}
		tree["transport"] = TransportStreamableHTTP
		tree["baseUrl"] = component["url"]
		tree["endpointPath"] = ""
	case "stdio":
		if hasAnyKey(component, "url", "headers", "staticHeaders") {
			return ServerDefinition{}, fmt.Errorf("stdio MCP cannot declare HTTP fields")
		}
		if command, ok := component["command"].(string); !ok || strings.TrimSpace(command) == "" {
			return ServerDefinition{}, fmt.Errorf("stdio command must be a non-empty string")
		}
		tree["transport"] = TransportStdio
	default:
		return ServerDefinition{}, fmt.Errorf("MCP type must be streamableHttp or stdio")
	}
	for _, key := range []string{"command", "args"} {
		if value, exists := component[key]; exists {
			tree[key] = value
		}
	}
	if value, exists := component["timeout"]; exists {
		timeout, ok := value.(float64)
		if !ok || timeout <= 0 || timeout != float64(int64(timeout)) {
			return ServerDefinition{}, fmt.Errorf("timeout must be positive integer milliseconds")
		}
		tree["read-timeout"] = int((int64(timeout) + 999) / 1000)
	} else {
		tree["read-timeout"] = 30
	}
	for _, pair := range [][2]string{{"headers", "staticHeaders"}, {"env", "staticEnv"}} {
		values := map[string]any{}
		seen := map[string]bool{}
		for _, field := range []string{pair[0], pair[1]} {
			raw, exists := component[field]
			if !exists {
				continue
			}
			entries, ok := raw.(map[string]any)
			if !ok {
				return ServerDefinition{}, fmt.Errorf("%s must be a string map", field)
			}
			for key, rawValue := range entries {
				value, ok := rawValue.(string)
				if !ok {
					return ServerDefinition{}, fmt.Errorf("%s values must be strings", field)
				}
				normalized := key
				if pair[0] == "headers" {
					normalized = strings.ToLower(key)
				}
				if seen[normalized] {
					return ServerDefinition{}, fmt.Errorf("conflicting %s field %q", pair[0], key)
				}
				seen[normalized] = true
				if field == pair[0] && credentialReady {
					var err error
					value, err = resolveConnectorCredential(pkg, value, credentials)
					if err != nil {
						return ServerDefinition{}, err
					}
				}
				if pair[0] == "headers" && strings.ContainsAny(value, "\r\n") {
					return ServerDefinition{}, fmt.Errorf("header contains newline")
				}
				values[key] = value
			}
		}
		if len(values) > 0 {
			tree[pair[0]] = values
		}
	}
	// Resolve bare names only from the connector's prepared bin directory.
	if command, ok := tree["command"].(string); ok && !filepath.IsAbs(command) && !strings.ContainsAny(command, `/\`) {
		tree["command"] = filepath.Join("bin", command)
	}
	server, err := parseServerTree(filepath.Join(pkg.Dir, "mcp.json"), tree)
	if err != nil {
		return ServerDefinition{}, err
	}
	if server.Transport == TransportStreamableHTTP {
		server.EndpointPath = ""
	}
	server.ConnectorID = pkg.ID
	server.ConnectorBinDir = pkg.BinDir
	if err := agentconfig.ValidateUserEnvironment(server.Env); err != nil {
		return ServerDefinition{}, err
	}
	server.DisabledTools, err = normalizeStringSlice(component["disabledTools"])
	if err != nil {
		return ServerDefinition{}, fmt.Errorf("disabledTools: %w", err)
	}
	if pkg.AuthMode != "none" && !(pkg.AuthMode == "token" && credentialReady) {
		// Account authorization is deliberately not inferred from process env or
		// CLI output. These modes require a configured credential provider.
		server.SetupError = "connector authentication requires setup: " + pkg.AuthMode
	}
	if _, exists := component["runtime"]; exists {
		server.SetupError = "connector runtime preparation is not implemented; provide a prepared command without runtime requirements"
	}
	return server, nil
}

func ValidateConnectorPackage(pkg connector.Package) error {
	for name := range pkg.MCP {
		if _, err := connectorServer(pkg, name); err != nil {
			return err
		}
	}
	return nil
}
