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

func IsMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), Mode)
}
