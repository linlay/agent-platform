package llm

import (
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/bashsec"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/hitl"
)

type approvalKind string

const (
	approvalKindFileAccess   approvalKind = "file_access"
	approvalKindFileWrite    approvalKind = "file_write"
	approvalKindBashSecurity approvalKind = "bash_security"
	approvalKindBashAccess   approvalKind = "bash_access"
	approvalKindHITL         approvalKind = "hitl"
)

type approvalFastPathMode int

const (
	approvalFastPathExecuteNow approvalFastPathMode = iota
	approvalFastPathSkipBatch
)

type approvalRequest struct {
	kind               approvalKind
	invocation         *preparedToolInvocation
	result             hitl.InterceptResult
	ruleTimeout        int
	fileAccessPlan     *filetools.AccessPlan
	fileWritePlan      *filetools.WritePlan
	bashSecurityReview *bashsec.ReviewResult
	bashAccessReview   *accesspolicy.BashPlan
}

func (s *llmRunStream) approvalRequestForInvocation(invocation *preparedToolInvocation) (approvalRequest, bool) {
	if accessPlan, writePlan, ok := s.combinedFileWriteApprovalPlans(invocation); ok {
		return approvalRequest{
			kind:           approvalKindFileAccess,
			invocation:     invocation,
			result:         fileWriteInterceptResult(*writePlan),
			fileAccessPlan: accessPlan,
			fileWritePlan:  writePlan,
		}, true
	}
	if plan := s.lookupFileAccessPlan(invocation); plan != nil && s.fileAccessPlanNeedsApproval(*plan) {
		return s.fileAccessApprovalRequest(invocation, *plan), true
	}
	if plan := s.lookupFileWritePlan(invocation); plan != nil && s.fileWritePlanNeedsApproval(*plan) {
		return s.fileWriteApprovalRequest(invocation, *plan), true
	}
	if review := s.lookupBashSecurityReview(invocation); review.Decision == bashsec.ReviewRequiresApproval {
		return s.bashSecurityApprovalRequest(invocation, review), true
	}
	if review := s.lookupBashAccessReview(invocation); review.RequiresApproval() {
		return s.bashAccessApprovalRequest(invocation, review), true
	}
	return approvalRequest{}, false
}

func (s *llmRunStream) fileAccessApprovalRequest(invocation *preparedToolInvocation, plan filetools.AccessPlan) approvalRequest {
	result := fileAccessInterceptResult(plan)
	if _, writePlan, ok := s.combinedFileWriteApprovalPlans(invocation); ok {
		result = fileWriteInterceptResult(*writePlan)
	}
	return approvalRequest{
		kind:           approvalKindFileAccess,
		invocation:     invocation,
		result:         result,
		fileAccessPlan: &plan,
	}
}

func (s *llmRunStream) fileWriteApprovalRequest(invocation *preparedToolInvocation, plan filetools.WritePlan) approvalRequest {
	return approvalRequest{
		kind:          approvalKindFileWrite,
		invocation:    invocation,
		result:        fileWriteInterceptResult(plan),
		fileWritePlan: &plan,
	}
}

func (s *llmRunStream) bashSecurityApprovalRequest(invocation *preparedToolInvocation, review bashsec.ReviewResult) approvalRequest {
	return approvalRequest{
		kind:               approvalKindBashSecurity,
		invocation:         invocation,
		result:             bashSecurityInterceptResult(invocation, review),
		bashSecurityReview: &review,
	}
}

func (s *llmRunStream) bashAccessApprovalRequest(invocation *preparedToolInvocation, review accesspolicy.BashPlan) approvalRequest {
	return approvalRequest{
		kind:             approvalKindBashAccess,
		invocation:       invocation,
		result:           bashAccessInterceptResult(invocation, review),
		bashAccessReview: &review,
	}
}

func hitlApprovalRequest(invocation *preparedToolInvocation, result hitl.InterceptResult) approvalRequest {
	return approvalRequest{
		kind:        approvalKindHITL,
		invocation:  invocation,
		result:      result,
		ruleTimeout: result.Rule.Timeout,
	}
}

func (s *llmRunStream) emitApprovalRequestDeltas(request approvalRequest) error {
	invocation := request.invocation
	result := request.result
	s.hitlPendingCall = invocation
	s.hitlMatch = &result
	s.hitlAwaitingID = buildHITLAwaitingID(invocation.toolID)

	args := s.approvalRequestArgs(request)
	s.hitlAwaitArgs = CloneMap(args)
	s.pending = append(s.pending, s.buildHITLAwaitDelta(s.hitlAwaitingID, args, request.ruleTimeout))

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

func (s *llmRunStream) approvalRequestArgs(request approvalRequest) map[string]any {
	if request.kind == approvalKindHITL {
		return s.buildHITLArgs(request.invocation, request.result)
	}
	return s.buildConfirmApprovalArgs(request.invocation, request.result)
}

func (s *llmRunStream) executeApprovedApprovalRequest(request approvalRequest) error {
	switch request.kind {
	case approvalKindFileAccess:
		if request.fileAccessPlan != nil {
			return s.executeApprovedFileAccessInvocation(request.invocation, *request.fileAccessPlan)
		}
	case approvalKindFileWrite:
		if request.fileWritePlan != nil {
			return s.executeApprovedFileWriteInvocation(request.invocation, *request.fileWritePlan)
		}
	case approvalKindBashSecurity:
		if request.bashSecurityReview != nil {
			return s.executeApprovedBashSecurityInvocation(request.invocation, *request.bashSecurityReview)
		}
	case approvalKindBashAccess:
		if request.bashAccessReview != nil {
			return s.executeApprovedBashAccessInvocation(request.invocation, *request.bashAccessReview)
		}
	case approvalKindHITL:
		return s.executeApprovedBashInvocation(request.invocation, request.result)
	}
	return s.executeOriginalBash(request.invocation)
}

func (r approvalRequest) hasApprovalDecision() bool {
	return r.invocation != nil && strings.TrimSpace(r.invocation.approvalDecision) != ""
}

func (s *llmRunStream) tryResolveApprovalFastPath(request approvalRequest, mode approvalFastPathMode) (bool, error) {
	if request.hasApprovalDecision() {
		if mode == approvalFastPathExecuteNow {
			return true, s.executeApprovedApprovalRequest(request)
		}
		return true, nil
	}
	switch request.kind {
	case approvalKindFileWrite:
		if request.fileWritePlan != nil && filetools.HasWriteApproval(s.execCtx, *request.fileWritePlan) {
			if mode == approvalFastPathExecuteNow {
				return true, s.executeOriginalBash(request.invocation)
			}
			return true, nil
		}
	case approvalKindBashSecurity:
		if request.bashSecurityReview == nil {
			return false, nil
		}
		return s.tryResolveBashSecurityApprovalFastPath(request, *request.bashSecurityReview, mode)
	case approvalKindBashAccess:
		if request.bashAccessReview == nil {
			return false, nil
		}
		return s.tryResolveBashAccessApprovalFastPath(request, *request.bashAccessReview, mode)
	}
	return false, nil
}

func (s *llmRunStream) tryResolveBashSecurityApprovalFastPath(request approvalRequest, review bashsec.ReviewResult, mode approvalFastPathMode) (bool, error) {
	if mode == approvalFastPathExecuteNow {
		if handled, err := s.executeSandboxBashSecurityOverride(request.invocation, review); handled {
			return true, err
		}
	} else {
		switch s.sandboxBashSecurityOverrideAction(request.invocation, review) {
		case "allow", "block":
			return true, nil
		case "auto":
			if s.engine != nil && s.engine.cfg.SandboxBash.Security.AuditAutoApprovals {
				s.applyHITLDecision(request.invocation, request.result, "", "auto_approved", sandboxBashSecurityOverrideReason, true)
			}
			return true, nil
		}
	}
	if s.isRuleWhitelisted(review.RuleKey) {
		s.applyHITLDecision(request.invocation, request.result, "", "approve_rule_run", "", true)
		if mode == approvalFastPathExecuteNow {
			s.registerBashSecurityApproval(review.Fingerprint)
			return true, s.executeOriginalBash(request.invocation)
		}
		return true, nil
	}
	if s.shouldAutoApproveBashSecurity(review) {
		if mode == approvalFastPathExecuteNow {
			s.registerBashSecurityApproval(review.Fingerprint)
			return true, s.executeOriginalBash(request.invocation)
		}
		return true, nil
	}
	if s.hasBashSecurityApproval(review.Fingerprint) {
		if mode == approvalFastPathExecuteNow {
			return true, s.executeOriginalBash(request.invocation)
		}
		return true, nil
	}
	return false, nil
}

func (s *llmRunStream) tryResolveBashAccessApprovalFastPath(request approvalRequest, review accesspolicy.BashPlan, mode approvalFastPathMode) (bool, error) {
	if s.isRuleWhitelisted(review.RuleKey) {
		s.applyHITLDecision(request.invocation, request.result, "", "approve_rule_run", "", true)
		accesspolicy.RegisterRuleApproval(s.execCtx, review.RuleKey)
		if mode == approvalFastPathExecuteNow {
			return true, s.executeOriginalBash(request.invocation)
		}
		return true, nil
	}
	if accesspolicy.HasApproval(s.execCtx, review) {
		if mode == approvalFastPathExecuteNow {
			return true, s.executeOriginalBash(request.invocation)
		}
		return true, nil
	}
	return false, nil
}

func (s *llmRunStream) tryResolveHITLApprovalFastPath(request approvalRequest, mode approvalFastPathMode) (bool, error) {
	if request.kind != approvalKindHITL || !strings.EqualFold(request.result.Rule.ViewportType, "builtin") {
		return false, nil
	}
	if s.isRuleWhitelisted(request.result.Rule.RuleKey) {
		s.applyHITLDecision(request.invocation, request.result, "", "approve_rule_run", "", true)
		if mode == approvalFastPathExecuteNow {
			return true, s.executeApprovedApprovalRequest(request)
		}
		return true, nil
	}
	if s.shouldAutoApproveHITL(request.result) {
		if mode == approvalFastPathExecuteNow {
			return true, s.executeOriginalBash(request.invocation)
		}
		return true, nil
	}
	return false, nil
}
