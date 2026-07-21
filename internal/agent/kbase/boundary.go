package kbase

import corekbase "agent-platform/internal/kbase"

const (
	ToolSearch   = corekbase.ToolSearch
	ToolFiles    = corekbase.ToolFiles
	ToolRead     = corekbase.ToolRead
	ToolStatus   = corekbase.ToolStatus
	ToolRefresh  = corekbase.ToolRefresh
	ToolDatetime = corekbase.ToolDatetime
)

func DefaultToolNames() []string {
	return corekbase.DefaultToolNames()
}

func IsTool(name string) bool {
	return corekbase.IsTool(name)
}

func FilterTools(tools []string) []string {
	return corekbase.FilterTools(tools)
}

// BoundaryPolicy is the KBASE mode-owned runtime boundary consumed by the
// catalog YAML adapter. Dedicated KBASE agents never carry memory state, and
// configured tools are constrained to the KBASE allowlist.
type BoundaryPolicy struct {
	ToolNames     []string
	MemoryEnabled bool
}

func ResolveBoundaryPolicy(toolNames []string) BoundaryPolicy {
	filtered := FilterTools(toolNames)
	if len(filtered) == 0 {
		filtered = DefaultToolNames()
	}
	return BoundaryPolicy{ToolNames: filtered, MemoryEnabled: false}
}
