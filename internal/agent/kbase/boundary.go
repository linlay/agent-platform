package kbase

import "agent-platform/internal/knowledge"

const (
	ToolSearch   = knowledge.ToolSearch
	ToolFiles    = knowledge.ToolFiles
	ToolRead     = knowledge.ToolRead
	ToolStatus   = knowledge.ToolStatus
	ToolDatetime = knowledge.ToolDatetime
)

var structuredFileToolNames = []string{
	"file_read",
	"file_glob",
	"file_grep",
	"file_write",
	"file_edit",
}

// CreateToolNames is the tool list written into agent.yml when a KBASE agent
// is created without one. Effective tools come from presets, explicit Agent
// declarations and mounted connectors; binding a library never adds tools.
// This legacy creation default is never applied at load time.
func CreateToolNames() []string {
	return append([]string{ToolDatetime}, structuredFileToolNames...)
}

// StructuredFileToolNames lists the file tools a KBASE agent needs to browse
// and edit its Workspace. The catalog warns when none of them is declared.
func StructuredFileToolNames() []string {
	return append([]string(nil), structuredFileToolNames...)
}
