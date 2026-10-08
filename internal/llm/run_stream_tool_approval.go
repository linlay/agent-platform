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
	mode := "approval"
	if plan.View != nil {
		mode = "form"
	}
	return approvalRequest{kind: approvalKindTool, invocation: invocation, toolApproval: plan, result: hitl.InterceptResult{Intercepted: true, Rule: hitl.FlatRule{Mode: mode, RuleKey: "tool-exact-" + plan.Fingerprint, Title: plan.Title}}}, true
}
func (s *llmRunStream) executeApprovedTool(request approvalRequest) error {
	invocation := request.invocation
	if request.toolApproval == nil || (invocation.approvalDecision != "approve" && invocation.approvalDecision != "auto_approved") {
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

func (s *llmRunStream) canAutoApproveTool(request approvalRequest) bool {
	if request.toolApproval == nil || !request.toolApproval.AllowAutoApprove {
		return false
	}
	level := s.currentAccessLevel()
	return level == AccessLevelAutoApprove || level == AccessLevelFullAccess
}

func (s *llmRunStream) autoApproveTool(request approvalRequest) error {
	request.invocation.shownApproval = &request
	s.applyHITLDecision(request.invocation, request.result, "", "auto_approved", "accessLevel="+s.currentAccessLevel(), true)
	return s.executeApprovedTool(request)
}
