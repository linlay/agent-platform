package mcp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/connector"
)

// ConvertLegacy is used only by the explicit migration command. Credentials
// are returned separately and must never be written inside the package.
func ConvertLegacy(path string, tree any) (connector.Manifest, map[string]any, map[string]string, error) {
	server, err := parseServerTree(path, tree)
	if err != nil {
		return connector.Manifest{}, nil, nil, err
	}
	root, _ := tree.(map[string]any)
	if server.Key == "" {
		copy := map[string]any{}
		for key, value := range root {
			copy[key] = value
		}
		copy["enabled"] = true
		server, err = parseServerTree(path, copy)
		if err != nil {
			return connector.Manifest{}, nil, nil, err
		}
	}
	if !connector.ValidID(server.Key) {
		return connector.Manifest{}, nil, nil, fmt.Errorf("legacy server %q cannot become a connector id", server.Key)
	}
	manifest := connector.Manifest{ID: server.Key, Name: server.Name, Version: "1.0.0", Type: "mcp", AuthMode: "none"}
	platform := map[string]any{"connect-timeout": server.ConnectTimeout, "startup-timeout": server.StartupTimeout, "retry": server.Retry}
	if enabled, ok := root["enabled"].(bool); ok && !enabled {
		platform["enabled"] = false
	}
	if server.ToolPrefix != "" {
		platform["toolPrefix"] = server.ToolPrefix
	}
	if len(server.AliasMap) > 0 {
		platform["aliasMap"] = server.AliasMap
	}
	if raw, ok := root["tools"]; ok {
		platform["tools"] = raw
	}
	if server.AuthSource != "" {
		platform["authSource"] = server.AuthSource
	}
	component := map[string]any{"timeout": server.ReadTimeout * 1000, "platform": platform}
	if server.Transport == TransportStdio {
		command, err := filepath.Abs(server.Command)
		if err != nil {
			return manifest, nil, nil, err
		}
		cwd, err := filepath.Abs(server.WorkingDir)
		if err != nil {
			return manifest, nil, nil, err
		}
		component["type"] = "stdio"
		component["command"] = command
		component["args"] = server.Args
		if hasAnyKey(root, "workingDirectory", "working-directory") {
			platform["workingDirectory"] = cwd
		}
	} else {
		component["type"] = "streamableHttp"
		component["url"] = server.ResolvedURL()
	}
	credentials := map[string]string{}
	fields := []map[string]any{}
	inject := func(values map[string]string, target string) {
		if len(values) == 0 {
			return
		}
		refs := map[string]string{}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := values[key]
			credentialKey := fmt.Sprintf("MIGRATED_%d", len(credentials)+1)
			credentials[credentialKey] = value
			refs[key] = "${" + credentialKey + "}"
			fields = append(fields, map[string]any{"key": credentialKey, "label": key, "type": "password", "required": true})
		}
		component[target] = refs
	}
	headers := map[string]string{}
	for key, value := range server.Headers {
		headers[key] = value
	}
	if server.AuthToken != "" {
		headers["Authorization"] = "Bearer " + server.AuthToken
	}
	inject(headers, "headers")
	inject(server.Env, "env")
	if len(credentials) > 0 {
		if server.AuthSource != "" {
			return manifest, nil, nil, fmt.Errorf("identity-file with additional credential fields requires explicit migration review")
		}
		manifest.AuthMode = "token"
		manifest.TokenSchema, _ = json.Marshal(map[string]any{"fields": fields})
	}
	return manifest, map[string]any{"mcpServers": map[string]any{"main": component}}, credentials, nil
}

func resolveConnectorCredential(pkg connector.Package, value string, credentials map[string]string) (string, error) {
	if !strings.Contains(value, "${") {
		return value, nil
	}
	var missing bool
	resolved := osExpand(value, func(key string) string {
		value, ok := credentials[key]
		if !ok {
			missing = true
		}
		return value
	})
	if missing {
		return "", fmt.Errorf("connector %s credential is not configured", pkg.ID)
	}
	return resolved, nil
}

// Only ${KEY} is substituted once; shell expressions and recursive expansion
// are not part of the package format.
func osExpand(value string, lookup func(string) string) string {
	var out strings.Builder
	for {
		start := strings.Index(value, "${")
		if start < 0 {
			out.WriteString(value)
			break
		}
		out.WriteString(value[:start])
		value = value[start+2:]
		end := strings.IndexByte(value, '}')
		if end < 0 {
			out.WriteString("${" + value)
			break
		}
		out.WriteString(lookup(value[:end]))
		value = value[end+1:]
	}
	return out.String()
}
