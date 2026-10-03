package llm

import (
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
)

func (s *llmRunStream) prepareToolApproval(invocation *preparedToolInvocation) {
	if invocation == nil || invocation.toolApprovalChecked {
		return
	}
	invocation.toolApprovalChecked = true
	if s.engine == nil || s.execCtx == nil {
		return
	}
	planner, ok := s.engine.tools.(ToolApprovalPlanner)
	if !ok {
		return
	}
	execution := *s.execCtx
	execution.CurrentToolID = invocation.toolID
	execution.CurrentToolName = invocation.toolName
	invocation.toolApproval, invocation.toolApprovalErr = planner.PrepareToolApproval(s.ctx, invocation.toolName, invocation.args, &execution)
}
func (s *llmRunStream) toolApprovalRequest(invocation *preparedToolInvocation) (approvalRequest, bool) {
	s.prepareToolApproval(invocation)
	if invocation == nil || invocation.toolApproval == nil {
		return approvalRequest{}, false
	}
	plan := invocation.toolApproval
	return approvalRequest{kind: approvalKindTool, invocation: invocation, toolApproval: plan, result: hitl.InterceptResult{Intercepted: true, Rule: hitl.FlatRule{Mode: "approval", RuleKey: "tool-exact-" + plan.Fingerprint, Title: plan.Title}}}, true
}
func (s *llmRunStream) executeApprovedTool(request approvalRequest) error {
	invocation := request.invocation
	if request.toolApproval == nil || invocation.approvalDecision != "approve" {
		s.appendOriginalToolResult(invocation, ToolExecutionResult{Error: "approval_rejected", Output: "this operation requires one-time approval", ExitCode: -1})
		return nil
	}
	if s.execCtx.ToolApprovals == nil {
		s.execCtx.ToolApprovals = map[string]bool{}
	}
	fingerprint := request.toolApproval.Fingerprint
	s.execCtx.ToolApprovals[fingerprint] = true
	defer delete(s.execCtx.ToolApprovals, fingerprint)
	return s.executeOriginalBash(invocation)
}
