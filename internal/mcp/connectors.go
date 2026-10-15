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

// EnabledConnectorServerKeys lists the source keys of components the registry
// synchronizes. Disabled components never receive a synchronization status.
func EnabledConnectorServerKeys(pkg connector.Package) []string {
	names := make([]string, 0, len(pkg.MCP))
	for name := range pkg.MCP {
		names = append(names, name)
	}
	sort.Strings(names)
	keys := make([]string, 0, len(names))
	for _, name := range names {
		if server, err := connectorServer(pkg, name); err == nil && !server.Enabled() {
			continue
		}
		keys = append(keys, connector.ServerKey(pkg.ID, name))
	}
	return keys
}

// The platform block carries explicit deployment settings migrated from the
// former registry (tool aliases/overrides and the platform identity adapter).
// It is not a second source of MCP definitions.
func connectorServer(pkg connector.Package, name string) (ServerDefinition, error) {
	// Credential templates live in mcp.json itself; work on a private copy.
	component := cloneAuthComponent(pkg.MCP[name])
	allowed := map[string]bool{}
	for _, key := range []string{"type", "url", "command", "args", "runtime", "timeout", "headers", "env", "disabledTools", "platform", "disabled"} {
		allowed[key] = true
	}
	for key := range component {
		if !allowed[key] {
			return ServerDefinition{}, fmt.Errorf("unknown MCP field %q", key)
		}
	}
	credentialReady := false
	if pkg.AuthMode == "token" {
		var err error
		_, credentialReady, err = connectorauth.TokenValues(pkg)
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
	// Desktop identity is declared by authMode oneid, never by a component flag.
	if pkg.AuthMode != connector.AuthOneID && tree["authSource"] != nil && tree["authSource"] != "" {
		return ServerDefinition{}, fmt.Errorf("platform authSource requires authMode oneid")
	}
	tree["serverKey"] = connector.ServerKey(pkg.ID, name)
	if disabled, exists := component["disabled"]; exists {
		value, ok := disabled.(bool)
		if !ok {
			return ServerDefinition{}, fmt.Errorf("disabled must be boolean")
		}
		tree["enabled"] = !value
	}
	switch component["type"] {
	case "streamableHttp":
		if hasAnyKey(component, "command", "args", "env", "runtime") {
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
				return ServerDefinition{}, fmt.Errorf("oneid cannot combine with another authSource")
			}
			tree["authSource"] = AuthSourceIdentityFile
		}
		tree["transport"] = TransportStreamableHTTP
		tree["baseUrl"] = component["url"]
		tree["endpointPath"] = ""
	case "stdio":
		if hasAnyKey(component, "url", "headers") {
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
	for _, field := range []string{"headers", "env"} {
		values := map[string]any{}
		seen := map[string]bool{}
		{
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
					// ONEID_TOKEN and AP_ACCESS_TOKEN are both supported Authorization templates.
					// Normalize the exact ONEID_TOKEN template to the shared in-memory form;
					// runtime identity and destination checks remain unchanged.
					if field == "headers" && strings.EqualFold(key, "Authorization") && value == "Bearer ${ONEID_TOKEN}" {
						value = "Bearer ${AP_ACCESS_TOKEN}"
					}
					resolved, err := resolveConnectorCredential(pkg, value, map[string]string{agentconfig.EnvAccessToken: "identity"})
					if err != nil || strings.Contains(resolved, "${") {
						return ServerDefinition{}, fmt.Errorf("oneid only supplies AP_ACCESS_TOKEN")
					}
					if field == "headers" && strings.EqualFold(key, "Authorization") && (field != "headers" || value != "Bearer ${AP_ACCESS_TOKEN}") {
						return ServerDefinition{}, fmt.Errorf("oneid manages Authorization; omit it or use Bearer ${AP_ACCESS_TOKEN}")
					}
				}
				normalized := key
				if field == "headers" {
					normalized = strings.ToLower(key)
				}
				if seen[normalized] {
					return ServerDefinition{}, fmt.Errorf("conflicting %s field %q", field, key)
				}
				seen[normalized] = true
				if field == "headers" && strings.ContainsAny(value, "\r\n") {
					return ServerDefinition{}, fmt.Errorf("header contains newline")
				}
				values[key] = value
			}
		}
		if len(values) > 0 {
			tree[field] = values
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
	if server.Transport == TransportStdio {
		values, err := pkg.CLIConfigEnvironment()
		if err != nil {
			return ServerDefinition{}, err
		}
		if len(values) > 0 && server.Env == nil {
			server.Env = map[string]string{}
		}
		for key, value := range values {
			server.Env[key] = value
		}
	}
	server.ConnectorID = pkg.ID
	server.ConnectorAuthRoot = pkg.PersistentRoot()
	server.ConnectorAuthMode = pkg.AuthMode
	server.ConnectorCredentialEnv = map[string]string{}
	if env, ok := component["env"].(map[string]any); ok {
		for key, value := range env {
			template, _ := value.(string)
			if strings.Contains(template, "${") {
				server.ConnectorCredentialEnv[key] = template
			}
		}
	}
	if pkg.AuthMode == connector.AuthToken || pkg.AuthMode == connector.AuthOAuth || pkg.AuthMode == connector.AuthMCP {
		server.ConnectorAuthRoot = pkg.PersistentRoot()
		server.CredentialRevision = credentialStateDigest(filepath.Join(pkg.PersistentRoot(), pkg.ID))
		if server.Transport == TransportStreamableHTTP {
			// Refresh rotates HTTP tokens without tearing down an in-flight SDK
			// session. Login/logout mutations have a separate durable revision.
			server.CredentialRevision = credentialStateDigest(filepath.Join(pkg.PersistentRoot(), pkg.ID, "auth-state.json"))
		}
	}
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
		selected, selectErr := connectorauth.OAuthComponent(pkg, name)
		if selectErr != nil {
			return ServerDefinition{}, selectErr
		}
		server.ConnectorOAuth = server.Transport == TransportStreamableHTTP
		server.ConnectorAuthRoot = pkg.PersistentRoot()
		server.ConnectorOAuthResource, err = connectorauth.OAuthResource(selected)
		if err != nil {
			return ServerDefinition{}, err
		}
		destination := server.ResolvedURL()
		if server.Transport == TransportStdio {
			destination = server.ConnectorOAuthResource
		}
		credentialReady = connectorauth.CredentialReady(server.ConnectorAuthRoot, pkg.ID, server.ConnectorOAuthResource, destination)
	}
	if server.ConnectorToken {
		server.ConnectorAuthRoot = pkg.PersistentRoot()
	}
	if (pkg.AuthMode == connector.AuthToken || pkg.AuthMode == connector.AuthOAuth || pkg.AuthMode == connector.AuthMCP) && !credentialReady {
		// Account authorization is deliberately not inferred from process env or
		// CLI output. These modes require a configured credential provider.
		server.SetupError = "connector authentication requires setup: " + string(pkg.AuthMode)
	}
	if pkg.MCPPreAuth == connector.MCPPreAuthCLI {
		// The CLI login revision restarts the component once login completes.
		server.CredentialRevision = connectorauth.CLIAuthorizationRevision(pkg)
		if !connectorauth.CLIAuthorized(pkg) {
			server.SetupError = "connector requires CLI login before its MCP components start"
		}
	}
	if _, exists := component["runtime"]; exists {
		server.SetupError = "connector runtime preparation is not implemented; provide a prepared command without runtime requirements"
	}
	return server, nil
}

func cloneAuthComponent(original map[string]any) map[string]any {
	copy := map[string]any{}
	for key, value := range original {
		if values, ok := value.(map[string]any); ok {
			inner := map[string]any{}
			for k, v := range values {
				inner[k] = v
			}
			copy[key] = inner
		} else {
			copy[key] = value
		}
	}
	return copy
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
