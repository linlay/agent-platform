// Package executor holds the template defaults of the EXECUTOR agent type: an
// ordinary native Agent meant to be called through /api/execute. It has no
// runtime behavior, prompt or stage of its own. What makes a Run deliver a
// checked result is the result_submit tool, which any native Agent may mount;
// EXECUTOR only provides it as a preset and starts Workspace editing off.
package executor

import "strings"

const Mode = "EXECUTOR"

// CreatePrefix names keys generated for executor agents created without one.
const CreatePrefix = "executor"

// WorkspaceReadOnly is the template editing default: an unattended Run edits
// a configured Workspace only when agent.yml asks for it.
const WorkspaceReadOnly = true

var createToolNames = []string{
	"datetime",
	"file_read",
	"file_write",
	"file_edit",
	"file_glob",
	"file_grep",
	"bash",
	"plan_add_tasks",
	"plan_update_task",
	"plan_get_tasks",
	"web_fetch",
	"vision_recognize",
	"result_submit",
}

// CreateToolNames lists recommended tools for clients creating an executor
// agent. It is not a load-time default; clients submit their concrete tool
// selection.
func CreateToolNames() []string {
	return append([]string(nil), createToolNames...)
}

func IsMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), Mode)
}
