package llm

import (
	"strings"

	"agent-platform/internal/bashsec"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/hitl"
)

func (s *llmRunStream) emitBashSecurityApprovalDeltas(invocation *preparedToolInvocation, review bashsec.ReviewResult) error {
	result := bashSecurityInterceptResult(invocation, review)
	s.hitlPendingCall = invocation
	s.hitlMatch = &result
	s.hitlAwaitingID = buildHITLAwaitingID(invocation.toolID)

	args := s.buildConfirmApprovalArgs(invocation, result)
	s.hitlAwaitArgs = CloneMap(args)
	s.pending = append(s.pending, s.buildHITLAwaitDelta(s.hitlAwaitingID, args, 0))

	if s.runControl != nil {
		awaitDelta, _ := s.pending[len(s.pending)-1].(DeltaAwaitAsk)
		s.runControl.ExpectSubmit(awaitingContextFromDeltaAsk(awaitDelta))
	}
	s.activeToolCall = nil
	if s.execCtx != nil {
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
	}
	return nil
}

func (s *llmRunStream) emitFileWriteApprovalDeltas(invocation *preparedToolInvocation, plan filetools.WritePlan) error {
	result := fileWriteInterceptResult(plan)
	s.hitlPendingCall = invocation
	s.hitlMatch = &result
	s.hitlAwaitingID = buildHITLAwaitingID(invocation.toolID)

	args := s.buildConfirmApprovalArgs(invocation, result)
	s.hitlAwaitArgs = CloneMap(args)
	s.pending = append(s.pending, s.buildHITLAwaitDelta(s.hitlAwaitingID, args, 0))

	if s.runControl != nil {
		awaitDelta, _ := s.pending[len(s.pending)-1].(DeltaAwaitAsk)
		s.runControl.ExpectSubmit(awaitingContextFromDeltaAsk(awaitDelta))
	}
	s.activeToolCall = nil
	if s.execCtx != nil {
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
	}
	return nil
}

func (s *llmRunStream) emitFileAccessApprovalDeltas(invocation *preparedToolInvocation, plan filetools.AccessPlan) error {
	result := fileAccessInterceptResult(plan)
	if _, writePlan, ok := s.combinedFileWriteApprovalPlans(invocation); ok {
		result = fileWriteInterceptResult(*writePlan)
	}
	s.hitlPendingCall = invocation
	s.hitlMatch = &result
	s.hitlAwaitingID = buildHITLAwaitingID(invocation.toolID)

	args := s.buildConfirmApprovalArgs(invocation, result)
	s.hitlAwaitArgs = CloneMap(args)
	s.pending = append(s.pending, s.buildHITLAwaitDelta(s.hitlAwaitingID, args, 0))

	if s.runControl != nil {
		awaitDelta, _ := s.pending[len(s.pending)-1].(DeltaAwaitAsk)
		s.runControl.ExpectSubmit(awaitingContextFromDeltaAsk(awaitDelta))
	}
	s.activeToolCall = nil
	if s.execCtx != nil {
		s.execCtx.CurrentToolID = ""
		s.execCtx.CurrentToolName = ""
	}
	return nil
}

func (s *llmRunStream) registerBashSecurityApproval(fingerprint string) {
	if s.execCtx == nil || strings.TrimSpace(fingerprint) == "" {
		return
	}
	if s.execCtx.BashSecurityApprovals == nil {
		s.execCtx.BashSecurityApprovals = map[string]int{}
	}
	s.execCtx.BashSecurityApprovals[fingerprint]++
}

func (s *llmRunStream) hasBashSecurityApproval(fingerprint string) bool {
	if s == nil || s.execCtx == nil || strings.TrimSpace(fingerprint) == "" || len(s.execCtx.BashSecurityApprovals) == 0 {
		return false
	}
	return s.execCtx.BashSecurityApprovals[fingerprint] > 0
}

func (s *llmRunStream) shouldAutoApproveBashSecurity(review bashsec.ReviewResult) bool {
	if s == nil || s.execCtx == nil || review.Level <= 0 {
		return false
	}
	return s.execCtx.HITLLevel >= review.Level
}

func bashSecurityInterceptResult(invocation *preparedToolInvocation, review bashsec.ReviewResult) hitl.InterceptResult {
	command := ""
	if invocation != nil {
		command = strings.TrimSpace(mapStringArg(invocation.args, "command"))
	}
	ruleKey := strings.TrimSpace(review.RuleKey)
	if ruleKey == "" {
		ruleKey = "bash-security::" + review.Fingerprint
	}
	level := review.Level
	if level <= 0 {
		level = 1
	}
	return hitl.InterceptResult{
		Intercepted: true,
		Rule: hitl.FlatRule{
			RuleKey:      ruleKey,
			Level:        level,
			Title:        "Bash security approval",
			ViewportType: "builtin",
			ViewportKey:  "approval",
		},
		OriginalCommand: command,
		MatchedCommand:  command,
		MatchedWhole:    true,
	}
}

func fileWriteInterceptResult(plan filetools.WritePlan) hitl.InterceptResult {
	title := "File write approval"
	if strings.EqualFold(strings.TrimSpace(plan.Operation), "edit") || strings.EqualFold(strings.TrimSpace(plan.ToolName), "file_edit") {
		title = "File edit approval"
	}
	return hitl.InterceptResult{
		Intercepted: true,
		Rule: hitl.FlatRule{
			RuleKey:      plan.RuleKey,
			Level:        2,
			Title:        title,
			ViewportType: "builtin",
			ViewportKey:  "approval",
		},
		OriginalCommand: plan.CommandText,
		MatchedCommand:  plan.CommandText,
		MatchedWhole:    true,
	}
}

func fileAccessInterceptResult(plan filetools.AccessPlan) hitl.InterceptResult {
	title := "File read approval"
	if plan.Mode == filetools.WriteAccess {
		title = "File path approval"
	}
	return hitl.InterceptResult{
		Intercepted: true,
		Rule: hitl.FlatRule{
			RuleKey:      plan.RuleKey,
			Level:        1,
			Title:        title,
			ViewportType: "builtin",
			ViewportKey:  "approval",
		},
		OriginalCommand: plan.CommandText,
		MatchedCommand:  plan.CommandText,
		MatchedWhole:    true,
	}
}
