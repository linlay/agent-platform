package platformcontrol

import (
	"fmt"
	"strings"

	"agent-platform/internal/connector"
)

func catalogResourceTypes() map[string]any {
	items := []map[string]any{}
	for _, typ := range []string{"agent", "team", "skill", "connector", "model", "provider", "tool", "mcp"} {
		editable := typ == "agent" || typ == "team" || typ == "skill" || typ == "connector"
		items = append(items, map[string]any{"resourceType": typ, "list": true, "get": true, "validate": editable, "apply": editable, "delete": editable && typ != "team"})
	}
	return map[string]any{"items": items, "limitations": []string{"Capabilities describe resource types; built-ins, calling Agent, references and source revisions further restrict writes.", "provider/model list valid means loaded locally, not credential verification or remote availability.", "mcp lists locally declared connector components, not Agent sessions or remotely discovered tools/resources/prompts; invalid connector packages fail enumeration.", "skill lists skill-center members; package metadata and Agent-local/connector-owned skills have no independent catalog target."}}
}

func publicMCPComponent(pkg connector.Package, name string) map[string]any {
	component := pkg.MCP[name]
	transport, _ := component["transport"].(string)
	transport = strings.ToLower(strings.TrimSpace(transport))
	if transport == "" {
		transport = "streamable-http"
	}
	if transport != "stdio" && transport != "streamable-http" {
		transport = "unknown"
	}
	enabled := true
	if value, ok := component["enabled"].(bool); ok {
		enabled = value
	}
	return map[string]any{"resourceType": "mcp", "resourceKey": pkg.ID + "/" + name, "valid": true, "diagnostics": nil, "editable": false, "definition": map[string]any{"connectorId": pkg.ID, "component": name, "transport": transport, "enabled": enabled, "scope": "connector-declaration", "availability": "not_checked", "builtin": pkg.Builtin}}
}

func (h *ToolHandler) readDiscoveryResource(typ, key string) (any, error) {
	if typ == "provider" {
		if h.models == nil {
			return nil, fmt.Errorf("models unavailable")
		}
		for _, v := range h.models.ProviderSummaries() {
			if v.Key == key {
				return map[string]any{"definition": v, "editable": false}, nil
			}
		}
		return nil, fmt.Errorf("provider not found")
	}
	id, name, ok := strings.Cut(key, "/")
	if !ok || !connector.ValidID(id) || !connector.ValidID(strings.ToLower(name)) {
		return nil, fmt.Errorf("MCP resourceKey must be connectorId/component")
	}
	pkg, err := h.cfg.Paths.ConnectorSources().Load(id)
	if err != nil {
		return nil, fmt.Errorf("MCP connector unavailable")
	}
	if _, ok := pkg.MCP[name]; !ok {
		return nil, fmt.Errorf("MCP component not found")
	}
	return publicMCPComponent(pkg, name), nil
}
