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

func (s *llmRunStream) emitApprovalRequestForInvocation(invocation *preparedToolInvocation) error {
	request, ok := s.approvalRequestForInvocation(invocation)
	if !ok {
		return s.executeOriginalBash(invocation)
	}
	return s.emitApprovalRequestDeltas(request)
}

func (r approvalRequest) hasApprovalDecision() bool {
	return r.invocation != nil && strings.TrimSpace(r.invocation.approvalDecision) != ""
}
