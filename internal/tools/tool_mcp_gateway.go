package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/mcp"
)

// MCP tool mode: the model reaches mounted MCP tools only through
// mcp_search and mcp_call. The session builder decides when they are offered;
// their behavior does not depend on the mode switch.

// mcpSearchResultBudget bounds the schemas returned by one search. Whole
// entries are omitted so every returned inputSchema stays complete.
const mcpSearchResultBudget = 32 * 1024

type mcpGatewayServers interface {
	ServerStatus(serverKey string) (api.MCPServerToolSyncStatus, bool)
	ServerIdentity(serverKey string) (connectorID string, component string, ok bool)
}

type mcpGatewayServer struct {
	key         string
	connectorID string
	component   string
	status      string
	message     string
}

// gatewayServers lists the caller's mounted MCP servers in a stable order.
func (r *ToolRouter) gatewayServers(execCtx *ExecutionContext) []mcpGatewayServer {
	if r == nil || execCtx == nil {
		return nil
	}
	catalog, _ := r.mcpTools.(mcpGatewayServers)
	servers := make([]mcpGatewayServer, 0, len(execCtx.Session.MCPServerKeys))
	for _, key := range execCtx.Session.MCPServerKeys {
		server := mcpGatewayServer{key: strings.TrimSpace(key), status: mcp.ToolSyncStatusPending}
		if server.key == "" {
			continue
		}
		if catalog != nil {
			if connectorID, component, ok := catalog.ServerIdentity(server.key); ok {
				server.connectorID, server.component = connectorID, component
			}
			if status, ok := catalog.ServerStatus(server.key); ok && status.Status != "" {
				server.status = status.Status
				if status.Diagnostic != nil {
					server.message = status.Diagnostic.Message
				}
			}
		}
		servers = append(servers, server)
	}
	sort.SliceStable(servers, func(i, j int) bool {
		return servers[i].connectorID+"/"+servers[i].component < servers[j].connectorID+"/"+servers[j].component
	})
	return servers
}

// gatewayTools returns the synchronized tools of the caller's mounted servers.
// Mounts are read from the frozen session on every call, never from the model.
func (r *ToolRouter) gatewayTools(execCtx *ExecutionContext) []api.ToolDetailResponse {
	if r == nil || r.mcpTools == nil || execCtx == nil || len(execCtx.Session.MCPServerKeys) == 0 {
		return nil
	}
	mounted := make(map[string]struct{}, len(execCtx.Session.MCPServerKeys))
	for _, key := range execCtx.Session.MCPServerKeys {
		mounted[strings.ToLower(strings.TrimSpace(key))] = struct{}{}
	}
	tools := []api.ToolDetailResponse{}
	for _, def := range r.mcpTools.Definitions() {
		if _, ok := mounted[strings.ToLower(strings.TrimSpace(AnyStringNode(def.Meta["serverKey"])))]; !ok {
			continue
		}
		if agentKey := AnyStringNode(def.Meta["agentKey"]); agentKey != "" && agentKey != execCtx.Session.AgentKey {
			continue
		}
		if _, ok := mcp.GatewayToolID(def); ok {
			tools = append(tools, def)
		}
	}
	return tools
}

func (r *ToolRouter) gatewayTarget(toolID string, execCtx *ExecutionContext) (api.ToolDetailResponse, bool) {
	for _, def := range r.gatewayTools(execCtx) {
		if mcp.MatchesGatewayToolID(def, toolID) {
			return def, true
		}
	}
	return api.ToolDetailResponse{}, false
}

// ResolveInvocationTarget lets scheduling and presentation follow the MCP tool
// an mcp_call reaches. Unresolvable calls fall back to mcp_call itself.
func (r *ToolRouter) ResolveInvocationTarget(toolName string, args map[string]any, execCtx *ExecutionContext) (InvocationTarget, bool) {
	if !strings.EqualFold(strings.TrimSpace(toolName), MCPCallToolName) {
		return InvocationTarget{}, false
	}
	toolID, _ := args["toolId"].(string)
	target, ok := r.gatewayTarget(toolID, execCtx)
	if !ok {
		return InvocationTarget{}, false
	}
	return InvocationTarget{Definition: target, Display: gatewayDisplay(target)}, true
}

func gatewayDisplay(def api.ToolDetailResponse) map[string]any {
	toolID, _ := mcp.GatewayToolID(def)
	return map[string]any{
		"toolId":      toolID,
		"connectorId": AnyStringNode(def.Meta["connectorId"]),
		"mcpServer":   AnyStringNode(def.Meta["mcpServer"]),
		"toolName":    AnyStringNode(def.Meta["mcpToolName"]),
	}
}

func (r *ToolRouter) invokeMCPSearch(args map[string]any, execCtx *ExecutionContext) ToolExecutionResult {
	for key := range args {
		if key != "query" && key != "connectorId" && key != "limit" {
			return modelToolError("invalid_arguments", "unknown field "+key, nil)
		}
	}
	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return modelToolError("invalid_arguments", "query must be a non-empty string", nil)
	}
	connectorID := ""
	if raw, exists := args["connectorId"]; exists {
		value, ok := raw.(string)
		if !ok {
			return modelToolError("invalid_arguments", "connectorId must be a string", nil)
		}
		connectorID = strings.TrimSpace(value)
	}
	limit := mcp.SearchDefaultLimit
	if raw, exists := args["limit"]; exists {
		value, ok := raw.(float64)
		if !ok || value != math.Trunc(value) || value < 1 || value > mcp.SearchMaxLimit {
			return modelToolError("invalid_arguments", fmt.Sprintf("limit must be an integer from 1 to %d", mcp.SearchMaxLimit), nil)
		}
		limit = int(value)
	}
	if execCtx == nil || len(execCtx.Session.MCPServerKeys) == 0 {
		return modelToolError("not_mounted", "this Agent has no mounted MCP connector", nil)
	}

	servers := r.gatewayServers(execCtx)
	pending := []map[string]any{}
	ready, matchedConnector := 0, connectorID == ""
	for _, server := range servers {
		if connectorID != "" && !strings.EqualFold(server.connectorID, connectorID) {
			continue
		}
		matchedConnector = true
		if server.status == mcp.ToolSyncStatusReady {
			ready++
			continue
		}
		entry := map[string]any{"connectorId": server.connectorID, "mcpServer": server.component, "status": server.status}
		if server.message != "" {
			entry["message"] = server.message
		}
		pending = append(pending, entry)
	}
	if !matchedConnector {
		return modelToolError("not_mounted", "connector "+connectorID+" is not mounted by this Agent", nil)
	}

	candidates := r.gatewayTools(execCtx)
	if connectorID != "" {
		filtered := candidates[:0]
		for _, def := range candidates {
			if strings.EqualFold(AnyStringNode(def.Meta["connectorId"]), connectorID) {
				filtered = append(filtered, def)
			}
		}
		candidates = filtered
	}
	hits := mcp.SearchTools(candidates, query, limit)
	tools := make([]map[string]any, 0, len(hits))
	used, omitted := 0, 0
	for _, def := range hits {
		entry := gatewaySearchEntry(def)
		data, _ := json.Marshal(entry)
		// The best hit is always returned; later hits must fit the budget whole.
		if len(tools) > 0 && used+len(data) > mcpSearchResultBudget {
			omitted++
			continue
		}
		used += len(data)
		tools = append(tools, entry)
	}

	status := "ok"
	switch {
	case ready == 0 && len(pending) > 0 && len(tools) == 0:
		status = "unavailable"
	case len(pending) > 0:
		status = "partial"
	case len(tools) == 0:
		status = "no_match"
	}
	payload := map[string]any{"status": status, "tools": tools}
	if len(pending) > 0 {
		payload["connectors"] = pending
	}
	if omitted > 0 {
		payload["omitted"] = omitted
	}
	return structuredResult(payload)
}

func gatewaySearchEntry(def api.ToolDetailResponse) map[string]any {
	display := gatewayDisplay(def)
	entry := map[string]any{
		"toolId":      display["toolId"],
		"connectorId": display["connectorId"],
		"mcpServer":   display["mcpServer"],
		"name":        display["toolName"],
		"description": def.Description,
		"inputSchema": gatewayInputSchema(def),
		"readOnly":    AnyBoolNode(def.Meta["readOnly"]),
	}
	if label := strings.TrimSpace(def.Label); label != "" {
		entry["label"] = label
	}
	return entry
}

func gatewayInputSchema(def api.ToolDetailResponse) map[string]any {
	if len(def.Parameters) == 0 {
		return map[string]any{"type": "object"}
	}
	return CloneMap(def.Parameters)
}

func (r *ToolRouter) invokeMCPCall(ctx context.Context, args map[string]any, execCtx *ExecutionContext) ToolExecutionResult {
	for key := range args {
		if key != "toolId" && key != "arguments" {
			return modelToolError("invalid_arguments", "unknown field "+key, nil)
		}
	}
	toolID, ok := args["toolId"].(string)
	if !ok || strings.TrimSpace(toolID) == "" {
		return modelToolError("invalid_arguments", "toolId must be a non-empty string", nil)
	}
	toolID = strings.TrimSpace(toolID)
	arguments, ok := args["arguments"].(map[string]any)
	if !ok {
		return modelToolError("invalid_arguments", "arguments must be an object; use {} when the tool takes none", nil)
	}
	connectorID, component, _, ok := mcp.ParseGatewayToolID(toolID)
	if !ok {
		return modelToolError("tool_not_found", "toolId must be connectorId/mcpServer/toolName as returned by mcp_search", nil)
	}
	target, found := r.gatewayTarget(toolID, execCtx)
	if !found {
		return r.gatewayTargetMissing(toolID, connectorID, component, execCtx)
	}
	if message, invalid := validateGatewayArguments(target, arguments); invalid {
		return modelToolError("invalid_arguments", message, map[string]any{"toolId": toolID, "inputSchema": gatewayInputSchema(target)})
	}
	payload, err := r.callMCPTool(ctx, target, arguments, execCtx)
	if err != nil {
		if errors.Is(err, ErrMCPCallOutcomeUnknown) {
			return modelToolError("result_unknown", "the call may have run remotely but no result arrived; check its effect before calling again: "+err.Error(), map[string]any{"toolId": toolID})
		}
		code := "server_unavailable"
		var callErr *mcpCallError
		if errors.As(err, &callErr) && callErr.code == "mcp_agent_mismatch" {
			code = "not_mounted"
		}
		return modelToolError(code, err.Error(), map[string]any{"toolId": toolID})
	}
	return normalizeMCPResult(toolID, payload)
}

func (r *ToolRouter) gatewayTargetMissing(toolID, connectorID, component string, execCtx *ExecutionContext) ToolExecutionResult {
	for _, server := range r.gatewayServers(execCtx) {
		if !strings.EqualFold(server.connectorID, connectorID) || !strings.EqualFold(server.component, component) {
			continue
		}
		if server.status != mcp.ToolSyncStatusReady {
			message := "MCP server " + connectorID + "/" + component + " is " + server.status
			if server.message != "" {
				message += ": " + server.message
			}
			return modelToolError("server_unavailable", message, map[string]any{"toolId": toolID})
		}
		return modelToolError("tool_not_found", "no tool "+toolID+"; use mcp_search to find the current tools", nil)
	}
	return modelToolError("not_mounted", "connector "+connectorID+"/"+component+" is not mounted by this Agent", nil)
}

// validateGatewayArguments checks arguments against the target's current
// schema. Schemas the validator cannot load are left to the remote server.
func validateGatewayArguments(target api.ToolDetailResponse, arguments map[string]any) (string, bool) {
	if len(target.Parameters) == 0 {
		return "", false
	}
	data, err := json.Marshal(target.Parameters)
	if err != nil {
		return "", false
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return "", false
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return "", false
	}
	// Validate the JSON form the remote server receives.
	instanceData, err := json.Marshal(arguments)
	if err != nil {
		return "", false
	}
	var instance any
	if err := json.Unmarshal(instanceData, &instance); err != nil {
		return "", false
	}
	if err := resolved.Validate(instance); err != nil {
		if strings.HasPrefix(err.Error(), "cannot validate version") {
			return "", false
		}
		return err.Error(), true
	}
	return "", false
}
