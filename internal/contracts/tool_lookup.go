package contracts

import "agent-platform-runner-go/internal/api"

type ToolDefinitionLookup interface {
	Tool(name string) (api.ToolDetailResponse, bool)
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
