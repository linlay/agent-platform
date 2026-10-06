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

var structuredFileToolNames = []string{
	"file_read",
	"file_glob",
	"file_grep",
	"file_write",
	"file_edit",
}

// CreateToolNames is the tool list written into agent.yml when a KBASE agent
// is created without one. A KBASE agent has no fixed tool boundary: its tools
// are exactly what agent.yml declares plus the knowledge-base capability
// tools every enabled KBASE receives, so this list supplies creation defaults and
// is never applied at load time.
func CreateToolNames() []string {
	return append([]string{ToolDatetime}, structuredFileToolNames...)
}

// StructuredFileToolNames lists the file tools a KBASE agent needs to browse
// and edit its Workspace. The catalog warns when none of them is declared.
func StructuredFileToolNames() []string {
	return append([]string(nil), structuredFileToolNames...)
}
