package testutil

import (
	"context"
	"encoding/json"

	"agent-platform/internal/contracts"
)

type NoopToolExecutor struct{}

func NewNoopToolExecutor() *NoopToolExecutor { return &NoopToolExecutor{} }

func (*NoopToolExecutor) Invoke(_ context.Context, toolName string, args map[string]any, _ *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	return contracts.ToolExecutionResult{
		Output:     "status: not_implemented",
		Structured: map[string]any{"toolName": toolName, "args": args, "status": "not_implemented"},
		Error:      "not_implemented",
		ExitCode:   -1,
	}, contracts.ErrNotImplemented
}

type NoopSandboxClient struct{}

func NewNoopSandboxClient() *NoopSandboxClient { return &NoopSandboxClient{} }

func (*NoopSandboxClient) OpenIfNeeded(context.Context, *contracts.ExecutionContext) error {
	return nil
}

func (*NoopSandboxClient) Execute(_ context.Context, _ *contracts.ExecutionContext, _ string, cwd string, _ int64, _ map[string]string) (contracts.SandboxExecutionResult, error) {
	return contracts.SandboxExecutionResult{
		ExitCode: -1,
		Stderr:   "status: not_implemented",
		Cwd:      cwd,
	}, contracts.ErrNotImplemented
}

func (*NoopSandboxClient) CloseQuietly(*contracts.ExecutionContext) {}

type NoopMcpClient struct{}

func NewNoopMcpClient() *NoopMcpClient { return &NoopMcpClient{} }

func (*NoopMcpClient) CallTool(_ context.Context, serverKey string, toolName string, args map[string]any, meta map[string]any) (any, error) {
	return map[string]any{"serverKey": serverKey, "toolName": toolName, "args": args, "meta": meta, "status": "not_implemented"}, nil
}

type NoopViewportClient struct{}

func NewNoopViewportClient() *NoopViewportClient { return &NoopViewportClient{} }

func (*NoopViewportClient) Get(_ context.Context, viewportKey string) (map[string]any, error) {
	return map[string]any{"viewportKey": viewportKey, "status": "not_implemented"}, nil
}

func MarshalPayload(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	data, _ := json.Marshal(value)
	return data
}
