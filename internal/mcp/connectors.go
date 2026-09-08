package mcp

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func loadConnectorServers(sources connector.Sources) (map[string]ServerDefinition, error) {
	packages, err := sources.LoadAll()
	if err != nil {
		return nil, err
	}
	servers := map[string]ServerDefinition{}
	for _, pkg := range packages {
		if err := connectorauth.ValidatePackage(pkg); err != nil {
			return nil, fmt.Errorf("connector %s authentication: %w", pkg.ID, err)
		}
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
		var err error
		credentials, credentialReady, err = connectorauth.TokenValues(pkg)
		if err != nil {
			return ServerDefinition{}, err
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
	if pkg.AuthMode == connector.AuthToken && tree["authSource"] != nil && tree["authSource"] != "" {
		return ServerDefinition{}, fmt.Errorf("token authentication cannot combine with platform authSource")
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
		if err := validateTokenQuery(pkg, parsed); err != nil {
			return ServerDefinition{}, err
		}
		if pkg.AuthMode == connector.AuthOneID {
			if err := validateIdentityFileRequest(parsed, parsed.Hostname()); err != nil {
				return ServerDefinition{}, err
			}
			if value := tree["authSource"]; value != nil && value != "" && value != AuthSourceIdentityFile {
				return ServerDefinition{}, fmt.Errorf("oneid-token cannot combine with another authSource")
			}
			tree["authSource"] = AuthSourceIdentityFile
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
				if pkg.AuthMode == connector.AuthOneID {
					if (field == "staticHeaders" || field == "staticEnv") && strings.Contains(value, "${") {
						return ServerDefinition{}, fmt.Errorf("oneid-token templates belong in headers/env, not static fields")
					}
					resolved, err := resolveConnectorCredential(pkg, value, map[string]string{agentconfig.EnvAccessToken: "identity"})
					if err != nil || strings.Contains(resolved, "${") {
						return ServerDefinition{}, fmt.Errorf("oneid-token only supplies AP_ACCESS_TOKEN")
					}
					if pair[0] == "headers" && strings.EqualFold(key, "Authorization") && (field != "headers" || value != "Bearer ${AP_ACCESS_TOKEN}") {
						return ServerDefinition{}, fmt.Errorf("oneid-token manages Authorization; omit it or use Bearer ${AP_ACCESS_TOKEN}")
					}
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
	server.ConnectorOneID = pkg.AuthMode == connector.AuthOneID
	server.ConnectorBinDir = pkg.BinDir
	server.ConnectorTokenQuery = pkg.AuthMode == connector.AuthToken && strings.Contains(server.ResolvedURL(), "${")
	server.ConnectorToken = pkg.AuthMode == connector.AuthToken && server.Transport == TransportStreamableHTTP
	if server.ConnectorToken {
		server.ConnectorTokenHeaders = map[string]string{}
		headers, _ := component["headers"].(map[string]any)
		for key, value := range headers {
			server.ConnectorTokenHeaders[key], _ = value.(string)
		}
	}
	if err := agentconfig.ValidateUserEnvironment(server.Env); err != nil {
		return ServerDefinition{}, err
	}
	server.DisabledTools, err = normalizeStringSlice(component["disabledTools"])
	if err != nil {
		return ServerDefinition{}, fmt.Errorf("disabledTools: %w", err)
	}
	if pkg.AuthMode == "oauth" || pkg.AuthMode == "mcp" {
		if err := connectorauth.ValidatePackage(pkg); err != nil {
			return ServerDefinition{}, err
		}
		server.ConnectorOAuth = true
		server.ConnectorAuthRoot = pkg.PersistentRoot()
		server.ConnectorOAuthResource, err = connectorauth.OAuthResource(pkg)
		if err != nil {
			return ServerDefinition{}, err
		}
		credentialReady = connectorauth.CredentialReady(server.ConnectorAuthRoot, pkg.ID, server.ConnectorOAuthResource, server.ResolvedURL())
	}
	if server.ConnectorToken {
		server.ConnectorAuthRoot = pkg.PersistentRoot()
	}
	if (pkg.AuthMode == connector.AuthToken || server.ConnectorOAuth) && !credentialReady {
		// Account authorization is deliberately not inferred from process env or
		// CLI output. These modes require a configured credential provider.
		server.SetupError = "connector authentication requires setup: " + string(pkg.AuthMode)
	}
	if _, exists := component["runtime"]; exists {
		server.SetupError = "connector runtime preparation is not implemented; provide a prepared command without runtime requirements"
	}
	return server, nil
}

func ValidateConnectorPackage(pkg connector.Package) error {
	if err := connectorauth.ValidatePackage(pkg); err != nil {
		return err
	}
	for name := range pkg.MCP {
		if _, err := connectorServer(pkg, name); err != nil {
			return err
		}
	}
	return nil
}

func ValidateConnectorPackages(packages []connector.Package) error {
	seen := map[string]bool{}
	for _, pkg := range packages {
		if err := ValidateConnectorPackage(pkg); err != nil {
			return err
		}
		for _, key := range pkg.ServerKeys() {
			if seen[key] {
				return fmt.Errorf("duplicate connector MCP server key %q", key)
			}
			seen[key] = true
		}
	}
	return nil
}
