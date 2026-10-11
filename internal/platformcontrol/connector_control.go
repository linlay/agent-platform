package platformcontrol

import (
	"context"
	"fmt"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/mcp"
	"agent-platform/internal/toolinput"
)

type ConnectorConnectionReader interface {
	Connection(context.Context, string) (connectorauth.Connection, error)
}

type ConnectorChecker interface {
	CheckConnector(context.Context, string, string) (mcp.ConnectorCheckResult, error)
}

func (h *ToolHandler) ConfigureConnectors(connections ConnectorConnectionReader, checker ConnectorChecker) *ToolHandler {
	h.connectorConnections, h.connectorChecker = connections, checker
	return h
}

func connectorNextAction(action, id, component string) map[string]any {
	result := map[string]any{"action": action, "connectorId": id}
	if component != "" {
		result["component"] = component
	}
	if action == "authorize" {
		result["route"] = "/connectors/" + id
	}
	return result
}

// Reading a source or completing a mutation adds only a local, credential-free
// snapshot. Snapshot failure cannot turn an already committed write into failure.
func (h *ToolHandler) addConnectorConnection(ctx context.Context, id string, result map[string]any) {
	pkg, pkgErr := h.cfg.Paths.ConnectorSources().Load(id)
	if pkgErr == nil {
		result["authMode"] = pkg.AuthMode
	}
	if h.connectorConnections == nil {
		result["connection"] = map[string]any{"readiness": "unknown", "authentication": map[string]any{"status": "unknown"}, "cached": true}
		result["diagnostics"] = []map[string]any{{"code": "authorization_status_unavailable", "message": "Connector authorization status is unavailable; the source operation succeeded."}}
		return
	}
	connection, err := h.connectorConnections.Connection(ctx, id)
	if err != nil {
		result["connection"] = map[string]any{"readiness": "unknown", "authentication": map[string]any{"status": "unknown"}, "cached": true}
		result["diagnostics"] = []map[string]any{{"code": "authorization_status_unavailable", "message": "Connector authorization status is unavailable; the source operation succeeded."}}
		return
	}
	result["authMode"] = connection.Capabilities.AuthMode
	result["connection"] = map[string]any{
		"connectorId": id, "configured": connection.Configured, "configurationRequired": connection.ConfigurationRequired, "readiness": connection.Readiness, "cached": true,
		"authentication": map[string]any{"status": connection.Authentication.Status, "componentId": connection.Authentication.ComponentID},
		"capabilities":   connection.Capabilities,
	}
	component := ""
	if pkgErr == nil && pkg.MCP["main"] != nil {
		component = "main"
	}
	if connection.Capabilities.AuthMode != connector.AuthNoAuth && (connection.Authentication.Status == "unauthorized" || connection.Authentication.Status == "pending" || connection.Authentication.Status == "preparing" || connection.Authentication.Status == "setup_required" || connection.Readiness == "configuration_required" || connection.Readiness == "authorization_required") {
		result["nextAction"] = connectorNextAction("authorize", id, component)
		return
	}
	if pkgErr == nil && pkg.MCP["main"] != nil && (connection.Capabilities.AuthMode == connector.AuthNoAuth || connection.Readiness == "ready") {
		result["nextAction"] = connectorNextAction("checkConnector", id, "main")
	}
}

func (h *ToolHandler) checkConnector(ctx context.Context, params map[string]any) (any, error) {
	id := stringValue(params, "connectorId")
	if !connector.ValidID(id) {
		return nil, toolinput.New("args.connectorId", "a valid connector ID from catalog_query", id, true, "Use the connector resourceKey returned by catalog_query.")
	}
	if h.connectorChecker == nil {
		return nil, fmt.Errorf("connector checker unavailable")
	}
	check, err := h.connectorChecker.CheckConnector(ctx, id, stringValue(params, "component"))
	if err != nil {
		return nil, fmt.Errorf("connector check unavailable")
	}
	result := map[string]any{
		"connectorId": check.ConnectorID, "component": check.Component, "authMode": check.AuthMode, "status": check.Status,
		"package": check.Package, "initialize": check.Initialize, "toolDiscovery": check.ToolDiscovery,
	}
	if check.ProtocolVersion != "" {
		result["protocolVersion"] = check.ProtocolVersion
	}
	if check.Status == "authorization_required" {
		result["nextAction"] = connectorNextAction("authorize", id, check.Component)
	}
	return result, nil
}
