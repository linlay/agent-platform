package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/hitl"
)

func (s *llmRunStream) reviewImageAccess(invocation *preparedToolInvocation) ([]filetools.AccessPlan, error) {
	if invocation == nil || s.engine == nil || (invocation.toolName != "image_generate" && invocation.toolName != "vision_recognize") {
		return nil, nil
	}
	reviewer, ok := s.engine.tools.(interface {
		ReviewImageAccess(string, map[string]any, *ExecutionContext) ([]filetools.AccessPlan, error)
	})
	if !ok {
		return nil, nil
	}
	ctx := ExecutionContext{}
	if s.execCtx != nil {
		ctx = *s.execCtx
	}
	ctx.Session = s.fileAccessSession()
	ctx.CurrentToolID = invocation.toolID
	return reviewer.ReviewImageAccess(invocation.toolName, invocation.args, &ctx)
}

func (s *llmRunStream) imageAccessApprovalRequest(invocation *preparedToolInvocation) (approvalRequest, bool) {
	plans, err := s.reviewImageAccess(invocation)
	if err != nil {
		return approvalRequest{}, false // Invalid/blocked sources fail in the executor.
	}
	var pending []filetools.AccessPlan
	for _, plan := range plans {
		if s.fileAccessPlanNeedsApproval(plan) {
			pending = append(pending, plan)
		}
	}
	if len(pending) == 0 {
		return approvalRequest{}, false
	}
	return approvalRequest{kind: approvalKindImageAccess, invocation: invocation, imageAccessPlans: pending, result: imageAccessInterceptResult(invocation, pending)}, true
}

func imageAccessInterceptResult(invocation *preparedToolInvocation, plans []filetools.AccessPlan) hitl.InterceptResult {
	keys := []string{invocation.toolName}
	paths := []string{invocation.toolName}
	for _, plan := range plans {
		keys = append(keys, plan.Fingerprint)
		paths = append(paths, plan.Path)
	}
	sum := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
	command := strings.Join(paths, "\n")
	return hitl.InterceptResult{Intercepted: true, OriginalCommand: command, MatchedCommand: command, MatchedWhole: true,
		Rule: hitl.FlatRule{RuleKey: "file-images:" + hex.EncodeToString(sum[:]), Title: "Image input read approval", ViewportType: "builtin", ViewportKey: "approval"}}
}

func (s *llmRunStream) executeApprovedImageAccess(request approvalRequest) error {
	invocation := request.invocation
	decision := strings.ToLower(strings.TrimSpace(invocation.approvalDecision))
	if decision == "reject" {
		s.appendOriginalToolResult(invocation, fileAccessDeniedToolResult(invocation, "file_read_denied"))
		return nil
	}
	if decision != "approve" && decision != "approve_rule_run" && decision != "auto_approved" {
		return s.emitApprovalRequestDeltas(request)
	}
	// Re-resolve all sources before granting anything: a symlink or argument
	// changed while waiting must not acquire a grant for an undisplayed target.
	current, err := s.reviewImageAccess(invocation)
	if err != nil {
		invocation.approvalDecision = ""
		return s.executeOriginalBash(invocation) // Preserve the executor's precise error.
	}
	approved := map[string]int{}
	for _, plan := range request.imageAccessPlans {
		approved[plan.Fingerprint]++
	}
	for _, plan := range current {
		if !s.fileAccessPlanNeedsApproval(plan) {
			continue
		}
		if decision == "auto_approved" || approved[plan.Fingerprint] == 0 {
			s.appendOriginalToolResult(invocation, ToolExecutionResult{Error: "approval_requirements_changed", Output: "Image input access changed after approval; submit a new tool call.", ExitCode: -1})
			return nil
		}
		approved[plan.Fingerprint]--
	}
	for _, plan := range request.imageAccessPlans {
		if decision == "approve_rule_run" {
			filetools.RegisterRuleReadApproval(s.execCtx, plan.RuleKey)
		} else if decision == "approve" {
			filetools.RegisterToolReadApproval(s.execCtx, invocation.toolID, plan.Fingerprint)
		}
	}
	defer filetools.ClearToolReadApprovals(s.execCtx, invocation.toolID)
	invocation.approvalDecision = ""
	return s.executeOriginalBash(invocation)
}
