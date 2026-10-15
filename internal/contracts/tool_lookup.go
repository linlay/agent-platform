package contracts

import "agent-platform/internal/api"

type ToolDefinitionLookup interface {
	Tool(name string) (api.ToolDetailResponse, bool)
}

// InvocationTarget is the tool a gateway invocation actually reaches.
// Scheduling policy and presentation follow Definition instead of the gateway.
type InvocationTarget struct {
	Definition api.ToolDetailResponse
	// Display identifies the target in tool.result events; never model input.
	Display map[string]any
}

// InvocationTargetResolver resolves gateway invocations, such as mcp_call,
// to their target tool without the caller knowing the gateway's arguments.
type InvocationTargetResolver interface {
	ResolveInvocationTarget(toolName string, args map[string]any, execCtx *ExecutionContext) (InvocationTarget, bool)
}

// CompositeToolLookup checks multiple ToolDefinitionLookup sources in order.
type CompositeToolLookup struct {
	sources []ToolDefinitionLookup
}

func NewCompositeToolLookup(sources ...ToolDefinitionLookup) *CompositeToolLookup {
	var filtered []ToolDefinitionLookup
	for _, s := range sources {
		if s != nil {
			filtered = append(filtered, s)
		}
	}
	return &CompositeToolLookup{sources: filtered}
}

func (c *CompositeToolLookup) Tool(name string) (api.ToolDetailResponse, bool) {
	for _, src := range c.sources {
		if tool, ok := src.Tool(name); ok {
			return tool, true
		}
	}
	return api.ToolDetailResponse{}, false
}
