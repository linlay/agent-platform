package contracts

import (
	"context"

	"agent-platform/internal/stream"
)

// NamedToolHandler owns a fixed set of tool names while leaving definition
// loading, execution policy, budgeting, retries, and timeouts to ToolRouter.
type NamedToolHandler interface {
	ToolNames() []string
	Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error)
}

// SourcePublication carries tool-produced sources without coupling the LLM
// runtime to a mode-specific structured result schema.
type SourcePublication struct {
	Kind    string
	Query   string
	Sources []stream.Source
}
