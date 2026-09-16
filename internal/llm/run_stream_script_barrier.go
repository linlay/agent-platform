package llm

import "agent-platform/internal/platformcontrol"

func hasToolExecutionBarrier(calls []*preparedToolInvocation) bool {
	if hasWriteExecutionBarrier(calls) {
		return true
	}
	for _, call := range calls {
		if call != nil {
			if descriptor, ok := platformcontrol.InvocationDescriptor(call.toolName, call.args); ok && descriptor.Barrier {
				return true
			}
		}
	}
	return false
}

// Preserve provider order for batches containing file mutation and execution.
// Writes must finish before dependent execution preflight (including HITL).
func hasWriteExecutionBarrier(calls []*preparedToolInvocation) bool {
	write, execute := false, false
	for _, call := range calls {
		if call == nil {
			continue
		}
		if call.toolName == "file_write" || call.toolName == "file_edit" {
			write = true
		}
		if isBashTool(call.toolName) {
			execute = true
		}
	}
	return write && execute
}
