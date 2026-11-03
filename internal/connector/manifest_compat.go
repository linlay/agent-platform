package connector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"agent-platform/internal/shellenv"
)

// TODO(connector-compat): 下个版本删除本文件及所有同标记的兼容接入和测试。
// 移除清单见 docs/连接器.md 的“清单临时兼容”节。
// Legacy declarations are normalized in memory; package files and canonical
// manifest/API JSON retain their existing format.
type legacyManifest struct {
	bindings map[string]legacyAuthBinding
}

type legacyAuthBinding struct {
	OAuth   json.RawMessage   `json:"oauth,omitempty"`
	Grant   string            `json:"grant,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

func decodeManifest(data []byte, manifest *Manifest) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return fmt.Errorf("connector manifest must be an object")
	}
	var legacy *legacyManifest
	for _, pair := range [][2]string{{"auth_mode", "authMode"}, {"auth_browser", "authBrowser"}, {"token_schema", "tokenSchema"}} {
		if raw, exists := fields[pair[0]]; exists {
			if _, conflict := fields[pair[1]]; conflict {
				return fmt.Errorf("connector manifest declares both %s and %s", pair[0], pair[1])
			}
			if pair[0] == "auth_mode" {
				var err error
				raw, err = normalizeLegacyAuthMode(raw)
				if err != nil {
					return err
				}
			}
			fields[pair[1]] = raw
			delete(fields, pair[0])
			legacy = &legacyManifest{}
		}
	}
	if _, exists := fields["authMode"]; !exists {
		// Omission used to delegate authentication to the connector.
		fields["authMode"] = json.RawMessage(`"self"`)
		legacy = &legacyManifest{}
	}
	var schema json.RawMessage
	if raw, exists := fields["tokenSchema"]; exists {
		var err error
		schema, err = normalizeLegacyTokenSchema(raw)
		if err != nil {
			return err
		}
		delete(fields, "tokenSchema")
		legacy = &legacyManifest{}
	}
	if raw, exists := fields["auth_bindings"]; exists {
		if legacy == nil {
			legacy = &legacyManifest{}
		}
		if err := decodeStrictJSON(raw, &legacy.bindings); err != nil {
			return fmt.Errorf("auth_bindings: %w", err)
		}
		delete(fields, "auth_bindings")
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	var decoded Manifest
	if err := decodeStrictJSON(normalized, &decoded); err != nil {
		return err
	}
	decoded.TokenSchema, decoded.legacy = schema, legacy
	if legacy != nil {
		if len(decoded.OAuth) != 0 {
			settings, err := normalizeLegacyOAuth(decoded.OAuth)
			if err != nil {
				return err
			}
			decoded.OAuth = settings
		}
		for target, binding := range legacy.bindings {
			if len(binding.OAuth) != 0 {
				settings, err := normalizeLegacyOAuth(binding.OAuth)
				if err != nil {
					return fmt.Errorf("auth_bindings.%s: %w", target, err)
				}
				if err := validateMCPAuthorizationSettings(settings); err != nil {
					return fmt.Errorf("auth_bindings.%s %w", target, err)
				}
				binding.OAuth = settings
				legacy.bindings[target] = binding
			}
		}
	}
	*manifest = decoded
	return nil
}

func normalizeLegacyAuthMode(raw json.RawMessage) (json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage(`"self"`), nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("auth_mode must be a string or null")
	}
	switch value {
	case "no_auth":
		value = string(AuthNoAuth)
	case "none", "cli":
		// In the former schema 'none' meant delegated, not unauthenticated.
		value = string(AuthDelegated)
	}
	return json.Marshal(value)
}

func normalizeLegacyTokenSchema(raw json.RawMessage) (json.RawMessage, error) {
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(raw, &schema); err != nil || schema == nil {
		return nil, fmt.Errorf("inline token schema must be an object")
	}
	var fields []map[string]json.RawMessage
	if err := json.Unmarshal(schema["fields"], &fields); err != nil {
		return nil, fmt.Errorf("inline token schema requires fields")
	}
	for _, field := range fields {
		if name, exists := field["name"]; exists {
			var nameValue string
			if err := json.Unmarshal(name, &nameValue); err != nil {
				return nil, fmt.Errorf("inline token schema field name must be a string")
			}
			if key, exists := field["key"]; exists {
				var keyValue string
				if err := json.Unmarshal(key, &keyValue); err != nil || keyValue != nameValue {
					return nil, fmt.Errorf("inline token schema field declares conflicting key and name")
				}
			}
			field["key"] = name
			delete(field, "name")
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	schema["fields"] = encoded
	return json.Marshal(schema)
}

// normalizeLegacyOAuth renames snake_case keys and drops the former discovery
// switch: the legacy auth_mode oauth is always MCP standard authorization now.
func normalizeLegacyOAuth(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("oauth must be an object")
	}
	for _, pair := range [][2]string{
		{"client_id", "clientId"}, {"authorization_endpoint", "authorizationEndpoint"},
		{"token_endpoint", "tokenEndpoint"}, {"redirect_uri", "redirectUri"},
		{"client_metadata_url", "clientMetadataUrl"},
	} {
		if value, exists := fields[pair[0]]; exists {
			if _, conflict := fields[pair[1]]; conflict {
				return nil, fmt.Errorf("oauth declares both %s and %s", pair[0], pair[1])
			}
			fields[pair[1]] = value
			delete(fields, pair[0])
		}
	}
	if raw, exists := fields["discovery"]; exists {
		var discovery bool
		if err := json.Unmarshal(raw, &discovery); err != nil {
			return nil, fmt.Errorf("oauth.discovery must be a boolean")
		}
		delete(fields, "discovery")
	}
	return json.Marshal(fields)
}

// LegacyOAuthBinding preserves per-component OAuth settings and CLI grant
// selection, which have no equivalent in the new component file format.
// TODO(connector-compat): 下个版本随本文件及 connectorauth 中的调用一起移除。
func (p Package) LegacyOAuthBinding(component string) (json.RawMessage, string) {
	if p.legacy == nil {
		return nil, ""
	}
	target := "mcp:" + component
	if component == "cli" {
		target = "cli"
	}
	binding := p.legacy.bindings[target]
	return binding.OAuth, binding.Grant
}

var legacyHeaderName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func (p *Package) normalizeLegacyComponents() error {
	if p.legacy == nil {
		return nil
	}
	// The old identity adapter also allowed HTTP MCP packages to omit auth_mode.
	if p.AuthMode == AuthDelegated && len(p.MCP) > 0 {
		identity := true
		for _, component := range p.MCP {
			platform, _ := component["platform"].(map[string]any)
			identity = identity && (component["type"] == "http" || component["type"] == "streamableHttp") && platform["authSource"] == "identity-file"
		}
		if identity {
			p.AuthMode = AuthOneID
		}
	}
	for _, component := range p.MCP {
		if component["type"] == "http" {
			component["type"] = "streamableHttp"
		}
	}
	allowed := p.credentialVariables()
	if len(p.legacy.bindings) > 0 && len(allowed) == 0 {
		return fmt.Errorf("auth_bindings require authMode token, mcp or oneid, but authMode is %s", p.AuthMode)
	}
	for target, binding := range p.legacy.bindings {
		if len(binding.OAuth) != 0 || binding.Grant != "" {
			if p.AuthMode != AuthMCP {
				return fmt.Errorf("auth_bindings.%s oauth/grant requires authMode mcp, but authMode is %s", target, p.AuthMode)
			}
		}
		var component map[string]any
		if target == "cli" {
			component = p.CLI
			if binding.Grant != "" && p.MCP[binding.Grant] == nil {
				return fmt.Errorf("auth_bindings.cli grant references missing MCP component %q", binding.Grant)
			}
		} else if name, ok := strings.CutPrefix(target, "mcp:"); ok {
			component = p.MCP[name]
			if binding.Grant != "" {
				return fmt.Errorf("auth_bindings.%s cannot declare grant; only cli may use another component's grant", target)
			}
		} else {
			return fmt.Errorf("auth_bindings target %q must be cli or mcp:<name>", target)
		}
		if component == nil {
			return fmt.Errorf("auth_bindings target %q references a missing component", target)
		}
		http := target != "cli" && component["type"] == "streamableHttp"
		if http && len(binding.Env) != 0 || !http && len(binding.Headers) != 0 {
			return fmt.Errorf("auth_bindings.%s does not match component transport: HTTP MCP accepts headers only, CLI and stdio MCP accept env only", target)
		}
		for field, entries := range map[string]map[string]string{"env": binding.Env, "headers": binding.Headers} {
			for key, template := range entries {
				if field == "env" && (!tokenFieldKey.MatchString(key) || shellenv.UnsafeOverride(key) || strings.HasPrefix(key, "AP_") || strings.HasSuffix(key, "_CONFIG_DIR")) {
					return fmt.Errorf("auth_bindings.%s env %q must be an uppercase identifier that is not reserved (AP_*, *_CONFIG_DIR or unsafe overrides)", target, key)
				}
				if field == "headers" {
					lower := strings.ToLower(key)
					if !legacyHeaderName.MatchString(key) || lower == "host" || lower == "content-length" || lower == "connection" || strings.HasPrefix(lower, "mcp-") || p.AuthMode != AuthToken {
						return fmt.Errorf("auth_bindings.%s header %q is not allowed: requires authMode token and a valid name other than Host, Content-Length, Connection or Mcp-*", target, key)
					}
				}
				// Legacy templates used the internal credential names.
				variables := allowed
				if target != "cli" {
					if p.AuthMode == AuthMCP {
						variables = map[string]bool{"ACCESS_TOKEN": true}
					} else if p.AuthMode == AuthOneID {
						variables = map[string]bool{shellenv.EnvIDToken: true}
					}
				} else if p.AuthMode == AuthMCP {
					template = strings.ReplaceAll(template, "${ACCESS_TOKEN}", "${"+EnvOAuthAccessToken+"}")
				}
				if err := validateLegacyTemplate(template, variables); err != nil {
					return fmt.Errorf("auth_bindings.%s %s %q: %w", target, field, key, err)
				}
				if err := mergeLegacyTemplate(component, field, key, template); err != nil {
					return fmt.Errorf("auth_bindings.%s: %w", target, err)
				}
			}
		}
	}
	return nil
}

func validateLegacyTemplate(template string, allowed map[string]bool) error {
	if strings.ContainsAny(template, "\r\n\x00") || !authVariable.MatchString(template) {
		return fmt.Errorf("value must be a single-line ${VAR} credential template")
	}
	values := map[string]string{}
	for key := range allowed {
		values[key] = "configured"
	}
	resolved, err := ResolveAuthTemplate(template, values)
	if err != nil || strings.Contains(resolved, "${") {
		return fmt.Errorf("template references a credential this authMode does not provide (allowed: %s)", strings.Join(sortedKeys(allowed), ", "))
	}
	return nil
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mergeLegacyTemplate(component map[string]any, field, key, template string) error {
	values, exists := component[field]
	if !exists {
		values = map[string]any{}
		component[field] = values
	}
	entries, ok := values.(map[string]any)
	if !ok {
		return fmt.Errorf("component %s must be an object", field)
	}
	for existing := range entries {
		if strings.EqualFold(existing, key) {
			return fmt.Errorf("conflicting credential binding for %s.%s", field, key)
		}
	}
	entries[key] = template
	return nil
}
