package kbase

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
