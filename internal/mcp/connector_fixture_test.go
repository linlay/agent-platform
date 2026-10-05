package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

// Tests use YAML shorthand to construct current connector.json/mcp.json fixtures.
// This builder is test-only; production never converts a YAML registry.
func writeConnectorFixture(path string, data []byte, mode os.FileMode) error {
	if filepath.Ext(path) != ".yml" && filepath.Ext(path) != ".yaml" {
		return os.WriteFile(path, data, mode)
	}
	if strings.Contains(filepath.Base(path), ".example.") {
		return nil
	}
	root := filepath.Dir(path)
	tree, err := config.LoadYAMLTreeBytes(data)
	if err != nil {
		return err
	}
	manifest, component, credentials, err := connectorFixtureDefinition(path, tree)
	if err != nil {
		id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "connector.json"), []byte("invalid: "+err.Error()), 0o644)
	}
	dir := filepath.Join(root, manifest.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, value := range map[string]any{"connector.json": manifest, "mcp.json": component} {
		bytes, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), bytes, mode); err != nil {
			return err
		}
	}
	if len(credentials) > 0 {
		credentialPath, err := connector.CredentialsPath((connector.Sources{ExternalRoot: root}).PersistentRoot(), manifest.ID)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(credentialPath), 0o700); err != nil {
			return err
		}
		bytes, _ := json.Marshal(credentials)
		if err := os.WriteFile(credentialPath, bytes, 0o600); err != nil {
			return err
		}
	}
	pkg, err := (connector.Sources{ExternalRoot: root}).Load(manifest.ID)
	if err != nil {
		return err
	}
	if _, err = pkg.SetConfigured(true); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, ".fixture-"+filepath.Base(path)), []byte(manifest.ID), 0o600)
}

func removeConnectorFixture(path string) error {
	marker := filepath.Join(filepath.Dir(path), ".fixture-"+filepath.Base(path))
	id, err := os.ReadFile(marker)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(filepath.Dir(path), string(id))); err != nil {
		return err
	}
	return os.Remove(marker)
}

func connectorFixtureDefinition(path string, tree any) (connector.Manifest, map[string]any, map[string]string, error) {
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
	manifest := connector.Manifest{ID: server.Key, Name: server.Name, Version: "1.0.0", Type: "mcp", AuthMode: connector.AuthDelegated}
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
		if server.AuthSource == AuthSourceIdentityFile {
			manifest.AuthMode = connector.AuthOneID
		}
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
