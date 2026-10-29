package kbase

import "agent-platform/internal/knowledge"

const (
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
// This creation default is never applied at load time.
func CreateToolNames() []string {
	return append([]string{ToolDatetime}, structuredFileToolNames...)
}
