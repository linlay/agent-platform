package engine

import (
	"context"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
)

type fakeMCPClient struct {
	payload map[string]any
}

func (f fakeMCPClient) CallTool(_ context.Context, serverKey string, toolName string, args map[string]any, meta map[string]any) (any, error) {
	return map[string]any{
		"serverKey": serverKey,
		"toolName":  toolName,
		"args":      args,
		"meta":      meta,
		"payload":   f.payload,
	}, nil
}

type fakeToolCatalog struct {
	defs []api.ToolDetailResponse
}

func (f fakeToolCatalog) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), f.defs...)
}

func (f fakeToolCatalog) Tool(name string) (api.ToolDetailResponse, bool) {
	for _, def := range f.defs {
		if def.Name == name || def.Key == name {
			return def, true
		}
	}
	return api.ToolDetailResponse{}, false
}

type fakeActionInvoker struct {
	result ToolExecutionResult
}

func (f fakeActionInvoker) Invoke(_ context.Context, actionName string, args map[string]any, _ *ExecutionContext) (ToolExecutionResult, error) {
	if f.result.Output == "" && len(f.result.Structured) == 0 {
		return structuredResult(map[string]any{"actionName": actionName, "args": args}), nil
	}
	return f.result, nil
}

func TestToolRouterRoutesMCPAndActionKinds(t *testing.T) {
	router := NewToolRouter(
		&testToolExecutor{},
		fakeMCPClient{payload: map[string]any{"status": "ok"}},
		fakeToolCatalog{defs: []api.ToolDetailResponse{
			{
				Key:         "mcp_tool",
				Name:        "mcp_tool",
				Description: "mcp",
				Meta:        map[string]any{"kind": "backend", "sourceType": "mcp", "serverKey": "server_a", "sourceKey": "server_a"},
			},
		}},
		NewFrontendSubmitCoordinator(),
		fakeActionInvoker{},
		api.ToolDetailResponse{
			Key:         "action_tool",
			Name:        "action_tool",
			Description: "action",
			Meta:        map[string]any{"kind": "action"},
		},
	)

	mcpResult, err := router.Invoke(context.Background(), "mcp_tool", map[string]any{"value": 1}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke mcp tool: %v", err)
	}
	if mcpResult.Structured["serverKey"] != "server_a" {
		t.Fatalf("expected routed mcp payload, got %#v", mcpResult.Structured)
	}

	actionResult, err := router.Invoke(context.Background(), "action_tool", map[string]any{"value": 2}, &ExecutionContext{})
	if err != nil {
		t.Fatalf("invoke action tool: %v", err)
	}
	if actionResult.Structured["actionName"] != "action_tool" {
		t.Fatalf("expected action invoker payload, got %#v", actionResult.Structured)
	}
}

func TestToolRouterFrontendSubmitWaitsForMatchingTool(t *testing.T) {
	router := NewToolRouter(
		&testToolExecutor{},
		fakeMCPClient{},
		nil,
		NewFrontendSubmitCoordinator(),
		fakeActionInvoker{},
		api.ToolDetailResponse{
			Key:         "confirm_dialog",
			Name:        "confirm_dialog",
			Description: "confirm",
			Meta:        map[string]any{"kind": "frontend", "toolType": "html", "viewportKey": "confirm_dialog", "sourceType": "local"},
		},
	)
	control := NewRunControl(context.Background(), "run_frontend")
	execCtx := &ExecutionContext{
		RunControl:    control,
		CurrentToolID: "tool_wait",
		Session: QuerySession{
			RunID: "run_frontend",
		},
	}

	done := make(chan ToolExecutionResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := router.Invoke(context.Background(), "confirm_dialog", map[string]any{"question": "continue?"}, execCtx)
		if err != nil {
			errCh <- err
			return
		}
		done <- result
	}()

	time.Sleep(20 * time.Millisecond)
	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:  "run_frontend",
		ToolID: "tool_wait",
		Params: map[string]any{"confirmed": true},
	})
	if !ack.Accepted {
		t.Fatalf("expected accepted submit ack, got %#v", ack)
	}

	select {
	case err := <-errCh:
		t.Fatalf("frontend tool returned error: %v", err)
	case result := <-done:
		if result.Structured["status"] != "submitted" {
			t.Fatalf("expected submitted result, got %#v", result.Structured)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for frontend submit")
	}
}
