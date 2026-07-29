package kbase

import (
	"strings"

	corekbase "agent-platform/internal/kbase"
)

const (
	ToolSearch   = corekbase.ToolSearch
	ToolFiles    = corekbase.ToolFiles
	ToolRead     = corekbase.ToolRead
	ToolStatus   = corekbase.ToolStatus
	ToolRefresh  = corekbase.ToolRefresh
	ToolDatetime = corekbase.ToolDatetime
)

var structuredFileToolNames = []string{
	"file_read",
	"file_glob",
	"file_grep",
	"file_write",
	"file_edit",
}

func DefaultToolNames() []string {
	return append(corekbase.DefaultToolNames(), structuredFileToolNames...)
}

func IsTool(name string) bool {
	if corekbase.IsTool(name) {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, toolName := range structuredFileToolNames {
		if normalized == toolName {
			return true
		}
	}
	return false
}

func FilterTools(tools []string) []string {
	filtered := make([]string, 0, len(tools))
	for _, toolName := range tools {
		if IsTool(toolName) {
			filtered = append(filtered, toolName)
		}
	}
	return filtered
}

// BoundaryPolicy is the KBASE mode-owned runtime boundary consumed by the
// catalog YAML adapter. Dedicated KBASE agents never carry memory state and
// always use the same fixed tool set in main and editing stages.
type BoundaryPolicy struct {
	ToolNames     []string
	MemoryEnabled bool
}

func ResolveBoundaryPolicy(_ []string) BoundaryPolicy {
	return BoundaryPolicy{ToolNames: DefaultToolNames(), MemoryEnabled: false}
}
