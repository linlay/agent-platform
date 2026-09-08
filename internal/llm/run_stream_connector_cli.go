package llm

import "agent-platform/internal/hitl"

// Check only the surrounding shell. OriginalCommand must remain executable
// source: neither approval replay nor form reconstruction may run a projection.
func (s *llmRunStream) checkBashHITL(invocation *preparedToolInvocation) hitl.InterceptResult {
	if s.checker == nil || invocation == nil {
		return hitl.InterceptResult{}
	}
	command := mapStringArg(invocation.args, "command")
	level := 0
	if s.execCtx != nil {
		level = s.execCtx.HITLLevel
	}
	if s.execCtx == nil || len(s.execCtx.Session.ConnectorCLIEntries) == 0 {
		return s.checker.Check(command, level)
	}
	plan := s.rawBashAccessReview(invocation)
	if plan.ConnectorOnly {
		return hitl.InterceptResult{}
	}
	result := s.checker.Check(plan.ShellReviewCommand(command), level)
	if plan.HasConnector && result.Intercepted {
		result.OriginalCommand = command
		// A residual match is never a form for the complete original command.
		result.MatchedWhole = false
	}
	return result
}
