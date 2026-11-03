package llm

import (
	"encoding/json"
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
	shellplan "agent-platform/internal/shellreview/plan"
)

// A grant belongs to one tool invocation, not to a cloneable run-wide counter.
// Only the execution context for this tool receives its one-shot approvals.
type bashAuthorization struct {
	arguments   string
	fingerprint string
	toolID      string
	access      map[string]int
	dispatched  bool
	retired     bool
}

func (s *llmRunStream) usesBashAuthorization(invocation *preparedToolInvocation) bool {
	if s == nil || s.execCtx == nil || invocation == nil || !strings.EqualFold(invocation.toolName, "bash") {
		return false
	}
	// View forms rebuild the command on submit and keep the serial path; a
	// refused call is reported by the authorization below.
	match := s.lookupPrecheckedHITL(invocation)
	return !match.Intercepted || match.Conflict != ""
}

// prepareBashAuthorization runs on the stream's scheduling goroutine. It
// grants/rechecks authorization without invoking a tool. A nil request means
// ready, or terminal when queuedResult is set. Repeated preflight is idempotent.
func (s *llmRunStream) prepareBashAuthorization(invocation *preparedToolInvocation) *approvalRequest {
	if invocation.queuedResult != nil {
		return nil
	}
	s.refreshAccessLevelForInvocation(invocation)
	a := invocation.bashAuthorization
	if a == nil {
		a = &bashAuthorization{toolID: invocation.toolID, access: map[string]int{}}
		invocation.bashAuthorization = a
	}
	if a.toolID != invocation.toolID || a.dispatched || a.retired {
		s.queueBashAuthorizationError(invocation, "bash_access_approval_required", "Bash authorization is no longer available for this invocation.")
		return nil
	}
	if strings.EqualFold(invocation.approvalDecision, "reject") || (invocation.hitlDecision != nil && invocation.hitlDecision.Decision == "reject") {
		result := hitlRejectedToolResult(invocation)
		invocation.queuedResult = &result
		return nil
	}
	if match := s.checkBashHITL(invocation); match.Conflict != "" {
		s.queueBashAuthorizationError(invocation, "view_intercept_rejected", match.Conflict)
		return nil
	}
	access := s.rawBashAccessReview(invocation)

	if access.Blocked() {
		s.queueBashAuthorizationError(invocation, access.ErrorCode(), access.Reason)
		return nil
	}

	if a.arguments != "" && a.arguments != bashArguments(invocation) {
		s.queueBashAuthorizationError(invocation, "bash_access_approval_required", "Bash arguments changed after approval; retry for a new review.")
		return nil
	}
	if invocation.approvalDecision != "" && invocation.shownApproval != nil {
		request := invocation.shownApproval
		decision := strings.ToLower(strings.TrimSpace(invocation.approvalDecision))
		if decision == "approve" || decision == "approve_rule_run" {
			if request.bashArguments != "" && request.bashArguments != bashArguments(invocation) {
				s.queueBashAuthorizationError(invocation, "bash_access_approval_required", "Bash arguments changed after approval; retry for a new review.")
				return nil
			}
			a.arguments, a.fingerprint = request.bashArguments, request.bashFingerprint
			if review := request.bashAccessReview; review != nil && review.RequiresApproval() {
				a.access[review.Fingerprint]++
				if decision == "approve_rule_run" && request.kind == approvalKindBashAccess {
					shellplan.RegisterRuleApproval(s.execCtx, request.result.Rule.RuleKey)
				}
			}

		}
		invocation.approvalDecision = ""
		invocation.shownApproval = nil
	}

	if a.fingerprint != "" && a.fingerprint != access.Fingerprint {
		s.queueBashAuthorizationError(invocation, "bash_access_approval_required", "Bash requirements changed after approval; retry for a new review.")
		return nil
	}

	// Move legacy exact grants, if any, rather than copying them to siblings.
	shellplan.TransferExactApprovals(s.execCtx, access, a.access)

	for _, rule := range shellplan.ApprovalRules(access) {
		if s.isRuleWhitelisted(rule) {
			shellplan.RegisterRuleApproval(s.execCtx, rule)
		}
	}
	ctx := s.bashReviewContext(invocation)
	if access.RequiresApproval() {
		if !shellplan.HasApproval(ctx, access) {
			request := s.bashAccessApprovalRequest(invocation, shellplan.PendingBashPlan(ctx, access))
			return s.bashApprovalNeeded(invocation, request, access.ErrorCode(), access.Reason)
		}
		if shellplan.BashApprovalSource(ctx, access) == "run_rule" {
			s.recordBashRuleApproval(invocation, bashAccessInterceptResult(invocation, access))
		}
	}
	invocation.approvalDecision = ""
	return nil
}

func (s *llmRunStream) bashApprovalNeeded(invocation *preparedToolInvocation, request approvalRequest, code, reason string) *approvalRequest {
	if invocation.hitlDecision != nil && invocation.hitlDecision.AwaitingID != "" {
		s.queueBashAuthorizationError(invocation, code, "Bash requirements changed after approval; retry for a new review. "+reason)
		return nil
	}
	// Keep the displayed primary rule; secondary gates receive tool-local grants
	// only. A run decision must never whitelist undisclosed secondary rules.
	request.bashArguments = bashArguments(invocation)
	request.bashFingerprint = s.rawBashAccessReview(invocation).Fingerprint
	if request.kind == approvalKindBashAccess && request.bashAccessReview != nil {
		if rules := shellplan.ApprovalRules(*request.bashAccessReview); len(rules) > 0 {
			request.result.Rule.RuleKey = rules[0]
		}
	}
	return &request
}

func (s *llmRunStream) queueBashAuthorizationError(invocation *preparedToolInvocation, code, reason string) {
	result := ToolExecutionResult{Output: reason, Error: code, ExitCode: -1}
	invocation.queuedResult = &result
	if invocation.hitlDecision != nil {
		invocation.hitlDecision.Executed = false
	}
}

func (s *llmRunStream) recordBashRuleApproval(invocation *preparedToolInvocation, match hitl.InterceptResult) {
	if invocation.hitlDecision == nil {
		s.applyHITLDecision(invocation, match, "", "approve_rule_run", "", true)
		invocation.approvalDecision = ""
	}
}

func (s *llmRunStream) bashReviewContext(invocation *preparedToolInvocation) *ExecutionContext {
	cloned := *s.execCtx
	cloned.AccessPolicyApprovals = invocation.bashAuthorization.access
	return &cloned
}

// Transfer once, after the budget check and immediately before dispatch. The
// executor consumes matching fingerprints only after its final launch checks.
func takeBashAuthorization(invocation *preparedToolInvocation, ctx *ExecutionContext) {
	a := invocation.bashAuthorization
	if a == nil {
		return
	}
	ctx.AccessPolicyApprovals = nil
	if a.toolID == invocation.toolID && !a.dispatched && !a.retired {
		ctx.AccessPolicyApprovals = a.access
		a.access = nil
		a.dispatched = true
	}
}

func (s *llmRunStream) invokeAuthorizedBash(invocation *preparedToolInvocation) error {
	if request := s.prepareBashAuthorization(invocation); request != nil {
		return s.emitApprovalRequestDeltas(*request)
	}
	if invocation.queuedResult != nil {
		result := *invocation.queuedResult
		invocation.queuedResult = nil
		s.appendOriginalToolResult(invocation, result)
		return nil
	}
	return s.invokeToolAndPublishResult(invocation)
}

// Tool arguments originate from JSON; map key order is stable when marshaled.
func bashArguments(invocation *preparedToolInvocation) string {
	encoded, err := json.Marshal(invocation.args)
	if err != nil {
		return "invalid:" + err.Error()
	}
	return string(encoded)
}
