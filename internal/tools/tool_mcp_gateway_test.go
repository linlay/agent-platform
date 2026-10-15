package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/llm"
	"agent-platform/internal/mcp"
	"agent-platform/internal/stream"
	"agent-platform/internal/toolpolicy"
)

type gatewayTestCatalog struct {
	defs     []api.ToolDetailResponse
	statuses map[string]string
	servers  map[string][2]string
}

func (c gatewayTestCatalog) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), c.defs...)
}

func (c gatewayTestCatalog) Tool(name string) (api.ToolDetailResponse, bool) {
	for _, def := range c.defs {
		if def.Name == name {
			return def, true
		}
	}
	return api.ToolDetailResponse{}, false
}

func (c gatewayTestCatalog) ServerStatus(key string) (api.MCPServerToolSyncStatus, bool) {
	status, ok := c.statuses[key]
	return api.MCPServerToolSyncStatus{Status: status}, ok
}

func (c gatewayTestCatalog) ServerIdentity(key string) (string, string, bool) {
	identity, ok := c.servers[key]
	return identity[0], identity[1], ok
}

type gatewayTestClient struct {
	calls   *[]string
	payload any
	err     error
}

func (c gatewayTestClient) CallTool(_ context.Context, serverKey, toolName string, args map[string]any, _ map[string]any) (any, error) {
	*c.calls = append(*c.calls, serverKey+"|"+toolName+"|"+MarshalJSON(args))
	return c.payload, c.err
}

func gatewayRemoteTool(serverKey, connectorID, name string, readOnly bool) api.ToolDetailResponse {
	meta := map[string]any{"sourceType": "mcp", "serverKey": serverKey, "agentKey": "agent", "connectorId": connectorID, "mcpServer": "main", "mcpToolName": name}
	if readOnly {
		meta["readOnly"] = true
	}
	return api.ToolDetailResponse{
		Name: "mcp_hash_" + name, Label: "标签 " + name, Description: "Tool " + strings.ReplaceAll(name, "_", " "),
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"title": map[string]any{"type": "string"}},
			"required":   []any{"title"},
		},
		Meta: meta,
	}
}

func gatewayEmbeddedDefs(t *testing.T) []api.ToolDetailResponse {
	t.Helper()
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	out := []api.ToolDetailResponse{}
	for _, def := range defs {
		if IsMCPGatewayToolName(def.Name) {
			out = append(out, def)
		}
	}
	if len(out) != 2 {
		t.Fatalf("embedded gateway definitions = %d", len(out))
	}
	return out
}

func newGatewayRouter(t *testing.T, client McpClient, catalog gatewayTestCatalog) *ToolRouter {
	t.Helper()
	return mustNewToolRouter(t, stubBackendToolExecutor{defs: gatewayEmbeddedDefs(t)}, client, catalog, nil)
}

func gatewayExecCtx(serverKeys ...string) *ExecutionContext {
	return &ExecutionContext{Session: QuerySession{AgentKey: "agent", ToolNames: append([]string(nil), MCPGatewayToolNames...), ToolSetFrozen: true, MCPServerKeys: serverKeys}}
}

func defaultGatewayCatalog() gatewayTestCatalog {
	return gatewayTestCatalog{
		defs: []api.ToolDetailResponse{
			gatewayRemoteTool("srv-docs", "kdocs", "create_document", false),
			gatewayRemoteTool("srv-docs", "kdocs", "search_files", true),
			gatewayRemoteTool("srv-map", "tencent-map", "geocode", true),
			gatewayRemoteTool("srv-other", "private", "create_secret", false),
		},
		statuses: map[string]string{"srv-docs": mcp.ToolSyncStatusReady, "srv-map": mcp.ToolSyncStatusReady, "srv-other": mcp.ToolSyncStatusReady},
		servers:  map[string][2]string{"srv-docs": {"kdocs", "main"}, "srv-map": {"tencent-map", "main"}, "srv-other": {"private", "main"}},
	}
}

func invokeGateway(t *testing.T, router *ToolRouter, tool string, args map[string]any, execCtx *ExecutionContext) ToolExecutionResult {
	t.Helper()
	result, err := router.Invoke(context.Background(), tool, args, execCtx)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMCPSearchReturnsMountedToolsWithSchemas(t *testing.T) {
	router := newGatewayRouter(t, nil, defaultGatewayCatalog())
	execCtx := gatewayExecCtx("srv-docs", "srv-map")
	result := invokeGateway(t, router, MCPSearchToolName, map[string]any{"query": "create document"}, execCtx)
	if result.Error != "" || result.Structured["status"] != "ok" {
		t.Fatalf("search = %#v", result)
	}
	tools := result.Structured["tools"].([]map[string]any)
	if len(tools) == 0 || tools[0]["toolId"] != "kdocs/main/create_document" || tools[0]["inputSchema"] == nil || tools[0]["label"] != "标签 create_document" {
		t.Fatalf("tools = %#v", tools)
	}
	for _, tool := range tools {
		if strings.HasPrefix(tool["toolId"].(string), "private/") {
			t.Fatal("an unmounted connector leaked into search")
		}
	}
	if strings.Contains(result.Output, "srv-docs") {
		t.Fatal("internal server keys must not reach the model")
	}

	result = invokeGateway(t, router, MCPSearchToolName, map[string]any{"query": "create", "connectorId": "tencent-map"}, execCtx)
	if result.Structured["status"] != "no_match" {
		t.Fatalf("connector filter = %#v", result.Structured)
	}
	result = invokeGateway(t, router, MCPSearchToolName, map[string]any{"query": "create", "connectorId": "private"}, execCtx)
	if result.Error != "not_mounted" {
		t.Fatalf("unmounted connector filter = %#v", result)
	}
	for _, args := range []map[string]any{{}, {"query": ""}, {"query": "x", "limit": float64(6)}, {"query": "x", "limit": 1.5}, {"query": "x", "extra": true}} {
		if result := invokeGateway(t, router, MCPSearchToolName, args, execCtx); result.Error != "invalid_arguments" {
			t.Fatalf("%#v -> %#v", args, result)
		}
	}
	if result := invokeGateway(t, router, MCPSearchToolName, map[string]any{"query": "x"}, gatewayExecCtx()); result.Error != "not_mounted" {
		t.Fatalf("no mounted MCP = %#v", result)
	}
}

func TestMCPSearchReportsUnsynchronizedConnectors(t *testing.T) {
	catalog := defaultGatewayCatalog()
	catalog.statuses["srv-map"] = mcp.ToolSyncStatusUnavailable
	router := newGatewayRouter(t, nil, catalog)
	result := invokeGateway(t, router, MCPSearchToolName, map[string]any{"query": "document"}, gatewayExecCtx("srv-docs", "srv-map"))
	connectors, _ := result.Structured["connectors"].([]map[string]any)
	if result.Structured["status"] != "partial" || len(connectors) != 1 || connectors[0]["connectorId"] != "tencent-map" || connectors[0]["status"] != "unavailable" {
		t.Fatalf("partial = %#v", result.Structured)
	}
	catalog.statuses["srv-docs"] = mcp.ToolSyncStatusSyncing
	catalog.defs = nil
	router = newGatewayRouter(t, nil, catalog)
	result = invokeGateway(t, router, MCPSearchToolName, map[string]any{"query": "document"}, gatewayExecCtx("srv-docs", "srv-map"))
	if result.Structured["status"] != "unavailable" {
		t.Fatalf("unavailable = %#v", result.Structured)
	}
}

func TestMCPSearchOmitsWholeEntriesOverBudget(t *testing.T) {
	catalog := defaultGatewayCatalog()
	huge := strings.Repeat("x", mcpSearchResultBudget)
	for i := 0; i < 3; i++ {
		def := gatewayRemoteTool("srv-docs", "kdocs", fmt.Sprintf("report_%d", i), true)
		def.Description = "report " + huge
		catalog.defs = append(catalog.defs, def)
	}
	router := newGatewayRouter(t, nil, catalog)
	result := invokeGateway(t, router, MCPSearchToolName, map[string]any{"query": "report", "limit": float64(3)}, gatewayExecCtx("srv-docs"))
	tools := result.Structured["tools"].([]map[string]any)
	if len(tools) != 1 || result.Structured["omitted"] != 2 || !strings.HasSuffix(tools[0]["description"].(string), "x") {
		t.Fatalf("budget = %d tools, omitted %#v", len(tools), result.Structured["omitted"])
	}
}

func TestMCPCallInvokesMountedTarget(t *testing.T) {
	calls := []string{}
	client := gatewayTestClient{calls: &calls, payload: map[string]any{"structuredContent": map[string]any{"documentId": "d1"}}}
	router := newGatewayRouter(t, client, defaultGatewayCatalog())
	execCtx := gatewayExecCtx("srv-docs")
	result := invokeGateway(t, router, MCPCallToolName, map[string]any{"toolId": "kdocs/main/create_document", "arguments": map[string]any{"title": "周报"}}, execCtx)
	if result.Error != "" || result.Structured["documentId"] != "d1" {
		t.Fatalf("call = %#v", result)
	}
	if len(calls) != 1 || calls[0] != `srv-docs|create_document|{"title":"周报"}` {
		t.Fatalf("remote calls = %#v", calls)
	}

	for _, tc := range []struct {
		args map[string]any
		code string
	}{
		{map[string]any{"toolId": "kdocs/main/create_document", "arguments": map[string]any{}}, "invalid_arguments"},
		{map[string]any{"toolId": "kdocs/main/create_document", "arguments": map[string]any{"title": 3}}, "invalid_arguments"},
		{map[string]any{"toolId": "kdocs/main/create_document"}, "invalid_arguments"},
		{map[string]any{"toolId": "kdocs/main/missing", "arguments": map[string]any{}}, "tool_not_found"},
		{map[string]any{"toolId": "kdocs/create_document", "arguments": map[string]any{}}, "tool_not_found"},
		{map[string]any{"toolId": "private/main/create_secret", "arguments": map[string]any{"title": "x"}}, "not_mounted"},
		{map[string]any{"toolId": "tencent-map/main/geocode", "arguments": map[string]any{"title": "x"}}, "not_mounted"},
	} {
		result := invokeGateway(t, router, MCPCallToolName, tc.args, execCtx)
		if result.Error != tc.code {
			t.Fatalf("%#v -> %#v", tc.args, result)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("rejected calls reached the server: %#v", calls)
	}
	result = invokeGateway(t, router, MCPCallToolName, map[string]any{"toolId": "kdocs/main/create_document", "arguments": map[string]any{}}, execCtx)
	if result.Structured["inputSchema"] == nil || result.Structured["toolId"] != "kdocs/main/create_document" {
		t.Fatalf("invalid arguments must carry the current schema: %#v", result.Structured)
	}
}

func TestMCPCallClassifiesRemoteFailures(t *testing.T) {
	catalog := defaultGatewayCatalog()
	calls := []string{}
	unknown := gatewayTestClient{calls: &calls, err: fmt.Errorf("%w: %w: timeout", ErrMCPCallFailed, ErrMCPCallOutcomeUnknown)}
	args := map[string]any{"toolId": "kdocs/main/create_document", "arguments": map[string]any{"title": "x"}}
	if result := invokeGateway(t, newGatewayRouter(t, unknown, catalog), MCPCallToolName, args, gatewayExecCtx("srv-docs")); result.Error != "result_unknown" {
		t.Fatalf("unknown = %#v", result)
	}
	refused := gatewayTestClient{calls: &calls, err: fmt.Errorf("%w: server refused", ErrMCPCallFailed)}
	if result := invokeGateway(t, newGatewayRouter(t, refused, catalog), MCPCallToolName, args, gatewayExecCtx("srv-docs")); result.Error != "server_unavailable" {
		t.Fatalf("refused = %#v", result)
	}
	if !errors.Is(unknown.err, ErrMCPCallFailed) {
		t.Fatal("unknown outcomes remain MCP call failures")
	}
	remoteError := gatewayTestClient{calls: &calls, payload: map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "quota exceeded"}}}}
	result := invokeGateway(t, newGatewayRouter(t, remoteError, catalog), MCPCallToolName, args, gatewayExecCtx("srv-docs"))
	if result.Error == "" || !strings.Contains(result.Output, "quota exceeded") || result.Structured["tool"] != "kdocs/main/create_document" {
		t.Fatalf("remote isError = %#v", result)
	}
	catalog.statuses["srv-docs"] = mcp.ToolSyncStatusUnavailable
	catalog.defs = nil
	if result := invokeGateway(t, newGatewayRouter(t, refused, catalog), MCPCallToolName, args, gatewayExecCtx("srv-docs")); result.Error != "server_unavailable" {
		t.Fatalf("unsynchronized server = %#v", result)
	}
}

func TestMCPCallReadOnlyPolicyFollowsTarget(t *testing.T) {
	calls := []string{}
	router := newGatewayRouter(t, gatewayTestClient{calls: &calls, payload: map[string]any{"structuredContent": map[string]any{"ok": true}}}, defaultGatewayCatalog())
	execCtx := gatewayExecCtx("srv-docs")
	execCtx.ToolExecutionPolicy = ToolExecutionPolicyReadOnly
	write := invokeGateway(t, router, MCPCallToolName, map[string]any{"toolId": "kdocs/main/create_document", "arguments": map[string]any{"title": "x"}}, execCtx)
	if write.Error != toolpolicy.DisabledErrorCode {
		t.Fatalf("write in read-only = %#v", write)
	}
	read := invokeGateway(t, router, MCPCallToolName, map[string]any{"toolId": "kdocs/main/search_files", "arguments": map[string]any{"title": "x"}}, execCtx)
	if read.Error != "" {
		t.Fatalf("read in read-only = %#v", read)
	}
	if search := invokeGateway(t, router, MCPSearchToolName, map[string]any{"query": "files"}, execCtx); search.Error != "" {
		t.Fatalf("search in read-only = %#v", search)
	}
	if len(calls) != 1 {
		t.Fatalf("remote calls = %#v", calls)
	}
}

func TestMCPCallTargetLabelReachesToolResultEvent(t *testing.T) {
	router := newGatewayRouter(t, nil, defaultGatewayCatalog())
	args := map[string]any{"toolId": "kdocs/main/create_document", "arguments": map[string]any{"title": "x"}}
	target, ok := router.ResolveInvocationTarget(MCPCallToolName, args, gatewayExecCtx("srv-docs"))
	if !ok || target.Definition.Name != "mcp_hash_create_document" || target.Display["toolId"] != "kdocs/main/create_document" {
		t.Fatalf("target = %#v %v", target, ok)
	}
	if _, ok := router.ResolveInvocationTarget(MCPCallToolName, args, gatewayExecCtx("srv-map")); ok {
		t.Fatal("unmounted target resolved")
	}
	if _, ok := router.ResolveInvocationTarget("bash", args, gatewayExecCtx("srv-docs")); ok {
		t.Fatal("non-gateway tool resolved")
	}

	mapper := llm.NewDeltaMapper("run", "chat", Budget{}, nil, nil)
	assembler := stream.NewAssembler(stream.StreamRequest{RunID: "run", ChatID: "chat", AgentKey: "agent"})
	var result stream.EventData
	for _, delta := range []AgentDelta{
		DeltaToolCall{ID: "call", Name: MCPCallToolName, ArgsDelta: MarshalJSON(args)},
		DeltaToolEnd{ToolIDs: []string{"call"}},
		DeltaToolResult{ToolID: "call", ToolName: MCPCallToolName, Result: ToolExecutionResult{Output: "ok"}, TargetLabel: target.Definition.Label, Target: target.Display},
	} {
		for _, input := range mapper.Map(delta) {
			for _, emission := range assembler.ConsumeEmissions(input) {
				if data := emission.Event.Data(); data.Type == "tool.result" {
					result = data
				}
			}
		}
	}
	targetPayload, _ := result.Value("target").(map[string]any)
	if result.String("toolLabel") != "标签 create_document" || targetPayload["toolName"] != "create_document" || targetPayload["connectorId"] != "kdocs" {
		t.Fatalf("tool.result = %#v", result.Payload)
	}
}
