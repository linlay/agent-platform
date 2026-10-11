package knowledge

const (
	Mode            = "KBASE"
	SourceKind      = "kbase"
	DefaultIconName = "kbase"
	ToolList        = "kbase_list"
	ToolSearch      = "kbase_search"
	ToolFiles       = "kbase_files"
	ToolRead        = "kbase_read"
	ToolStatus      = "kbase_status"
	ToolDatetime    = "datetime"
)

var capabilityToolNames = []string{
	ToolList,
	ToolSearch,
	ToolFiles,
	ToolRead,
	ToolStatus,
}

func CapabilityToolNames() []string {
	return append([]string(nil), capabilityToolNames...)
}

func DefaultToolNames() []string {
	return append(CapabilityToolNames(), ToolDatetime)
}
