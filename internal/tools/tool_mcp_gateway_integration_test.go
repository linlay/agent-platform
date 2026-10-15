package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/mcp"
	runtimesession "agent-platform/internal/runtime/session"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type gatewayIntegrationMounts []connector.AgentRuntime

func (m gatewayIntegrationMounts) ConnectorRuntimes() []connector.AgentRuntime { return m }

func TestMCPGatewayWithConnectorTransport(t *testing.T) {
	var calls atomic.Int32
	upstream := sdk.NewServer(&sdk.Implementation{Name: "gateway-integration", Version: "1"}, nil)
	upstream.AddTool(&sdk.Tool{
		Name: "search_docs", Description: "搜索文档",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		var args map[string]any
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &sdk.CallToolResult{StructuredContent: map[string]any{"query": args["query"]}}, nil
	})
	endpoint := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return upstream }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer endpoint.Close()
	root := t.TempDir()
	sources := connector.Sources{ExternalRoot: filepath.Join(root, "connectors-center"), StateRoot: filepath.Join(root, ".state", "connectors")}
	dir := filepath.Join(sources.ExternalRoot, "docs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	component, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"main": map[string]any{"type": "streamableHttp", "url": endpoint.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"connector.json": []byte(`{"id":"docs","name":"Docs","version":"1.0.0","type":"mcp","authMode":"self"}`),
		"mcp.json":       component,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := sources.Load("docs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pkg.SetConfigured(true); err != nil {
		t.Fatal(err)
	}
	registry, err := mcp.NewAgentRegistry(sources)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.BindAgents(gatewayIntegrationMounts{{AgentKey: "agent", ID: "docs", Dir: dir}}); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClientWithGate(registry, endpoint.Client(), nil)
	defer client.Close()
	syncer := mcp.NewToolSync(registry, client)
	router := mustNewToolRouter(t, stubBackendToolExecutor{defs: gatewayEmbeddedDefs(t)}, client, syncer, nil)
	servers := registry.Servers()
	if len(servers) != 1 {
		t.Fatalf("servers = %#v", servers)
	}
	agent := catalog.AgentDefinition{ConnectorMCPServers: []string{servers[0].Key}}
	names, mounted := runtimesession.SessionMCPTools(nil, agent, true, func() []string {
		t.Fatal("gateway mode listed direct MCP tools")
		return nil
	})
	if !reflect.DeepEqual(names, MCPGatewayToolNames) {
		t.Fatalf("gateway tools = %#v", names)
	}
	execCtx := &ExecutionContext{Session: QuerySession{AgentKey: "agent", ToolNames: names, ToolSetFrozen: true, MCPServerKeys: mounted}, ToolExecutionPolicy: "read_only"}
	// A Run admitted before synchronization can discover tools once ready.
	if got := invokeGateway(t, router, MCPSearchToolName, map[string]any{"query": "文档"}, execCtx); got.Structured["status"] != "unavailable" {
		t.Fatalf("before sync: %#v", got)
	}
	defs, err := syncer.Load(t.Context())
	if err != nil || len(defs) != 1 {
		t.Fatalf("sync: %#v %v", defs, err)
	}
	id, ok := mcp.GatewayToolID(defs[0])
	if !ok || id != "docs/main/search_docs" {
		t.Fatalf("toolId = %q", id)
	}
	search := invokeGateway(t, router, MCPSearchToolName, map[string]any{"query": "文档"}, execCtx)
	hits, _ := search.Structured["tools"].([]map[string]any)
	if search.Structured["status"] != "ok" || len(hits) != 1 || hits[0]["toolId"] != id || hits[0]["inputSchema"] == nil {
		t.Fatalf("search: %#v", search)
	}
	invalid := invokeGateway(t, router, MCPCallToolName, map[string]any{"toolId": id, "arguments": map[string]any{"query": 42}}, execCtx)
	if invalid.Error != "invalid_arguments" || calls.Load() != 0 {
		t.Fatalf("invalid arguments reached upstream: %#v calls=%d", invalid, calls.Load())
	}
	args := map[string]any{"toolId": id, "arguments": map[string]any{"query": "验证"}}
	result := invokeGateway(t, router, MCPCallToolName, args, execCtx)
	if result.Error != "" || result.Structured["query"] != "验证" || calls.Load() != 1 {
		t.Fatalf("call: %#v calls=%d", result, calls.Load())
	}
	// IDs can be reused, but a different Run must still mount the connector.
	unmounted := gatewayExecCtx()
	if got := invokeGateway(t, router, MCPCallToolName, args, unmounted); got.Error != "not_mounted" || calls.Load() != 1 {
		t.Fatalf("unmounted call: %#v calls=%d", got, calls.Load())
	}
	direct, keys := runtimesession.SessionMCPTools(nil, agent, false, func() []string { return router.MCPToolNamesForServers(agent.ConnectorMCPServers) })
	if !reflect.DeepEqual(direct, []string{defs[0].Name}) || keys != nil {
		t.Fatalf("default mode: %#v keys=%#v", direct, keys)
	}
}
