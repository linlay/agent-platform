package connector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
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
			settings, discovery, err := normalizeLegacyOAuth(decoded.OAuth)
			if err != nil {
				return err
			}
			decoded.OAuth = settings
			if decoded.AuthMode == AuthOAuth && discovery {
				decoded.AuthMode = AuthMCP
			}
		}
		for target, binding := range legacy.bindings {
			if len(binding.OAuth) != 0 {
				settings, _, err := normalizeLegacyOAuth(binding.OAuth)
				if err != nil {
					return err
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

func normalizeLegacyOAuth(raw json.RawMessage) (json.RawMessage, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false, fmt.Errorf("oauth must be an object")
	}
	for _, pair := range [][2]string{
		{"client_id", "clientId"}, {"authorization_endpoint", "authorizationEndpoint"},
		{"token_endpoint", "tokenEndpoint"}, {"redirect_uri", "redirectUri"},
		{"client_metadata_url", "clientMetadataUrl"},
	} {
		if value, exists := fields[pair[0]]; exists {
			if _, conflict := fields[pair[1]]; conflict {
				return nil, false, fmt.Errorf("oauth declares both %s and %s", pair[0], pair[1])
			}
			fields[pair[1]] = value
			delete(fields, pair[0])
		}
	}
	var discovery bool
	if raw, exists := fields["discovery"]; exists {
		if err := json.Unmarshal(raw, &discovery); err != nil {
			return nil, false, fmt.Errorf("oauth discovery must be boolean")
		}
		delete(fields, "discovery")
	}
	encoded, err := json.Marshal(fields)
	return encoded, discovery, err
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
		return fmt.Errorf("auth_bindings require token, oauth, mcp or oneid mode")
	}
	for target, binding := range p.legacy.bindings {
		if len(binding.OAuth) != 0 || binding.Grant != "" {
			if p.AuthMode != AuthOAuth && p.AuthMode != AuthMCP {
				return fmt.Errorf("OAuth bindings require oauth or mcp mode")
			}
		}
		var component map[string]any
		if target == "cli" {
			component = p.CLI
			if binding.Grant != "" && p.MCP[binding.Grant] == nil {
				return fmt.Errorf("CLI grant references missing MCP component")
			}
		} else if name, ok := strings.CutPrefix(target, "mcp:"); ok {
			component = p.MCP[name]
			if binding.Grant != "" {
				return fmt.Errorf("only CLI may consume another component grant")
			}
		} else {
			return fmt.Errorf("auth binding target must be cli or mcp:<name>")
		}
		if component == nil {
			return fmt.Errorf("auth binding targets missing component %q", target)
		}
		http := target != "cli" && component["type"] == "streamableHttp"
		if http && len(binding.Env) != 0 || !http && len(binding.Headers) != 0 {
			return fmt.Errorf("auth binding does not match component transport")
		}
		for field, entries := range map[string]map[string]string{"env": binding.Env, "headers": binding.Headers} {
			for key, template := range entries {
				if field == "env" && (!tokenFieldKey.MatchString(key) || shellenv.UnsafeOverride(key) || strings.HasPrefix(key, "AP_") || strings.HasSuffix(key, "_CONFIG_DIR")) {
					return fmt.Errorf("invalid credential environment variable")
				}
				if field == "headers" {
					lower := strings.ToLower(key)
					if !legacyHeaderName.MatchString(key) || lower == "host" || lower == "content-length" || lower == "connection" || strings.HasPrefix(lower, "mcp-") || p.AuthMode != AuthToken {
						return fmt.Errorf("invalid authentication header")
					}
				}
				// Legacy templates used the internal credential names.
				variables := allowed
				if target != "cli" {
					if p.AuthMode == AuthOAuth || p.AuthMode == AuthMCP {
						variables = map[string]bool{"ACCESS_TOKEN": true}
					} else if p.AuthMode == AuthOneID {
						variables = map[string]bool{"AP_ACCESS_TOKEN": true}
					}
				} else if p.AuthMode == AuthOAuth || p.AuthMode == AuthMCP {
					template = strings.ReplaceAll(template, "${ACCESS_TOKEN}", "${"+EnvOAuthAccessToken+"}")
				} else if p.AuthMode == AuthOneID {
					template = strings.ReplaceAll(template, "${AP_ACCESS_TOKEN}", "${"+EnvOneIDToken+"}")
				}
				if err := validateLegacyTemplate(template, variables); err != nil {
					return err
				}
				if err := mergeLegacyTemplate(component, field, key, template); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateLegacyTemplate(template string, allowed map[string]bool) error {
	if strings.ContainsAny(template, "\r\n\x00") || !authVariable.MatchString(template) {
		return fmt.Errorf("auth binding requires a credential template")
	}
	values := map[string]string{}
	for key := range allowed {
		values[key] = "configured"
	}
	resolved, err := ResolveAuthTemplate(template, values)
	if err != nil || strings.Contains(resolved, "${") {
		return fmt.Errorf("auth binding references an undeclared credential")
	}
	return nil
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
