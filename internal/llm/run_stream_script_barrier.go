package llm

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
