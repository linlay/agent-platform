package kbase

import "strings"

const (
	Mode            = "KBASE"
	SourceKind      = "kbase"
	DefaultIconName = "kbase"
	ToolSearch      = "kbase_search"
	ToolFiles       = "kbase_files"
	ToolRead        = "kbase_read"
	ToolStatus      = "kbase_status"
	ToolRefresh     = "kbase_refresh"
	ToolDatetime    = "datetime"
)

var capabilityToolNames = []string{
	ToolSearch,
	ToolFiles,
	ToolRead,
	ToolStatus,
	ToolRefresh,
}

func CapabilityToolNames() []string {
	return append([]string(nil), capabilityToolNames...)
}

func DefaultToolNames() []string {
	return append(CapabilityToolNames(), ToolDatetime)
}

func IsTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ToolSearch, ToolFiles, ToolRead, ToolStatus, ToolRefresh, ToolDatetime:
		return true
	default:
		return false
	}
}

func FilterTools(tools []string) []string {
	if len(tools) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(tools))
	for _, tool := range tools {
		if IsTool(tool) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}
