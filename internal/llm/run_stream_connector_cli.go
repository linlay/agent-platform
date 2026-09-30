package llm

import "agent-platform/internal/hitl"

// Execution trust never suppresses business approval rules.
func (s *llmRunStream) checkBashHITL(invocation *preparedToolInvocation) hitl.InterceptResult {
	if s.checker == nil || invocation == nil {
		return hitl.InterceptResult{}
	}
	command := mapStringArg(invocation.args, "command")
	if checker, ok := s.checker.(interface {
		CheckAll(string) []hitl.InterceptResult
	}); ok {
		return hitl.CombineRequirements(command, checker.CheckAll(command))
	}
	return s.checker.Check(command, 0)
}
