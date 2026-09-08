package llm

import (
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/bashsec"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
)

// A grant belongs to one tool invocation, not to a cloneable run-wide counter.
// Only the execution context for this tool receives its one-shot approvals.
type hostBashAuthorization struct {
	toolID      string
	access      map[string]int
	security    map[string]int
	hitlCommand string
	hitlRule    string
	dispatched  bool
	retired     bool
}

func (s *llmRunStream) usesHostBashAuthorization(invocation *preparedToolInvocation) bool {
	if s == nil || s.execCtx == nil || invocation == nil || !strings.EqualFold(invocation.toolName, "bash") || s.isSandboxRuntime() {
		return false
	}
	// Forms can rebuild commands on submit and keep their existing serial path.
	match := s.lookupPrecheckedHITL(invocation)
	return !match.Intercepted || match.Rule.IsBuiltinApproval()
}

// prepareHostBashAuthorization runs on the stream's scheduling goroutine. It
// grants/rechecks authorization without invoking a tool. A nil request means
// ready, or terminal when queuedResult is set. Repeated preflight is idempotent.
func (s *llmRunStream) prepareHostBashAuthorization(invocation *preparedToolInvocation) *approvalRequest {
	if invocation.queuedResult != nil {
		return nil
	}
	s.refreshAccessLevelForInvocation(invocation)
	a := invocation.hostBashAuthorization
	if a == nil {
		a = &hostBashAuthorization{toolID: invocation.toolID, access: map[string]int{}, security: map[string]int{}}
		invocation.hostBashAuthorization = a
	}
	if a.toolID != invocation.toolID || a.dispatched || a.retired {
		s.queueHostBashAuthorizationError(invocation, "bash_access_approval_required", "Bash authorization is no longer available for this invocation.")
		return nil
	}
	if strings.EqualFold(invocation.approvalDecision, "reject") || (invocation.hitlDecision != nil && invocation.hitlDecision.Decision == "reject") {
		result := hitlRejectedToolResult(invocation)
		invocation.queuedResult = &result
		return nil
	}
	if invocation.approvalDecision != "" && invocation.shownApproval != nil {
		request := invocation.shownApproval
		decision := strings.ToLower(strings.TrimSpace(invocation.approvalDecision))
		if decision == "approve" || decision == "approve_rule_run" {
			if review := request.bashAccessReview; review != nil && review.RequiresApproval() {
				if decision == "approve_rule_run" {
					s.grantDisplayedBashAccess(decision, *review)
				} else {
					a.access[review.Fingerprint] = 1
				}
			}
			if review := request.bashSecurityReview; review != nil {
				a.security[review.Fingerprint] = 1
				if decision == "approve_rule_run" {
					s.registerRuleWhitelist(review.RuleKey)
				}
			}
			if request.kind == approvalKindHITL {
				a.hitlCommand = request.result.OriginalCommand
				a.hitlRule = request.result.Rule.RuleKey
			}
		}
		invocation.approvalDecision = ""
		invocation.shownApproval = nil
	}

	command := strings.TrimSpace(mapStringArg(invocation.args, "command"))
	access := s.rawBashAccessReview(invocation)
	security := access.SecurityReview(command, s.knownRuntimeVariables())
	if security.Decision == bashsec.ReviewBlock {
		result := bashSecurityBlockedToolResult(security)
		invocation.queuedResult = &result
		return nil
	}
	if access.Blocked() {
		s.queueHostBashAuthorizationError(invocation, "bash_access_blocked", access.Reason)
		return nil
	}

	// Move legacy exact grants, if any, rather than copying them to siblings.
	reserveBashAccessApprovals(s.execCtx, access, a.access)
	moveOneShotApproval(s.execCtx.BashSecurityApprovals, a.security, security.Fingerprint)
	if security.Decision == bashsec.ReviewRequiresApproval && a.security[security.Fingerprint] == 0 {
		if s.isRuleWhitelisted(security.RuleKey) || s.shouldAutoApproveBashSecurity(security) {
			a.security[security.Fingerprint] = 1
			if s.isRuleWhitelisted(security.RuleKey) {
				s.recordHostBashRuleApproval(invocation, bashSecurityInterceptResult(invocation, security))
			}
		} else {
			request := s.bashSecurityApprovalRequest(invocation, security)
			return s.hostBashApprovalNeeded(invocation, request, "bash_security_approval_required", security.Reason)
		}
	}
	for _, rule := range accesspolicy.ApprovalRules(access) {
		if s.isRuleWhitelisted(rule) {
			accesspolicy.RegisterRuleApproval(s.execCtx, rule)
		}
	}
	ctx := s.hostBashReviewContext(invocation)
	if access.RequiresApproval() {
		if !accesspolicy.HasApproval(ctx, access) {
			request := s.bashAccessApprovalRequest(invocation, accesspolicy.PendingBashPlan(ctx, access))
			return s.hostBashApprovalNeeded(invocation, request, "bash_access_approval_required", access.Reason)
		}
		if accesspolicy.BashApprovalSource(ctx, access) == "run_rule" {
			s.recordHostBashRuleApproval(invocation, bashAccessInterceptResult(invocation, access))
		}
	}
	if s.checker != nil {
		match := s.checkBashHITL(invocation)
		if match.Intercepted && !(a.hitlCommand == command && a.hitlRule == match.Rule.RuleKey) {
			if s.isRuleWhitelisted(match.Rule.RuleKey) || s.shouldAutoApproveHITL(match) {
				a.hitlCommand, a.hitlRule = command, match.Rule.RuleKey
				if s.isRuleWhitelisted(match.Rule.RuleKey) {
					s.recordHostBashRuleApproval(invocation, match)
				}
			} else {
				request := hitlApprovalRequest(invocation, match)
				request.bashAccessReview = &access
				return &request
			}
		}
	}
	invocation.approvalDecision = ""
	return nil
}

func (s *llmRunStream) hostBashApprovalNeeded(invocation *preparedToolInvocation, request approvalRequest, code, reason string) *approvalRequest {
	if invocation.hitlDecision != nil && invocation.hitlDecision.AwaitingID != "" {
		s.queueHostBashAuthorizationError(invocation, code, "Bash requirements changed after approval; retry for a new review. "+reason)
		return nil
	}
	return &request
}

func (s *llmRunStream) queueHostBashAuthorizationError(invocation *preparedToolInvocation, code, reason string) {
	result := ToolExecutionResult{Output: reason, Error: code, ExitCode: -1}
	invocation.queuedResult = &result
	if invocation.hitlDecision != nil {
		invocation.hitlDecision.Executed = false
	}
}

func (s *llmRunStream) recordHostBashRuleApproval(invocation *preparedToolInvocation, match hitl.InterceptResult) {
	if invocation.hitlDecision == nil {
		s.applyHITLDecision(invocation, match, "", "approve_rule_run", "", true)
		invocation.approvalDecision = ""
	}
}

func moveOneShotApproval(source, target map[string]int, fingerprint string) {
	if fingerprint == "" || target[fingerprint] > 0 || source[fingerprint] <= 0 {
		return
	}
	target[fingerprint] = 1
	if source[fingerprint] == 1 {
		delete(source, fingerprint)
	} else {
		source[fingerprint]--
	}
}

func reserveBashAccessApprovals(ctx *ExecutionContext, plan accesspolicy.BashPlan, target map[string]int) {
	if !plan.RequiresApproval() || ctx.AccessPolicyRuleApprovals[plan.RuleKey] || target[plan.Fingerprint] > 0 {
		return
	}
	if ctx.AccessPolicyApprovals[plan.Fingerprint] > 0 {
		moveOneShotApproval(ctx.AccessPolicyApprovals, target, plan.Fingerprint)
		return
	}
	for _, leaf := range plan.Requirements {
		reserveBashAccessApprovals(ctx, leaf, target)
	}
}

func (s *llmRunStream) hostBashReviewContext(invocation *preparedToolInvocation) *ExecutionContext {
	cloned := *s.execCtx
	cloned.AccessPolicyApprovals = invocation.hostBashAuthorization.access
	cloned.BashSecurityApprovals = invocation.hostBashAuthorization.security
	return &cloned
}

// Transfer once, after the budget check and immediately before dispatch. The
// executor consumes matching fingerprints only after its final launch checks.
func takeHostBashAuthorization(invocation *preparedToolInvocation, ctx *ExecutionContext) {
	a := invocation.hostBashAuthorization
	if a == nil {
		return
	}
	ctx.AccessPolicyApprovals, ctx.BashSecurityApprovals = nil, nil
	if a.toolID == invocation.toolID && !a.dispatched && !a.retired {
		ctx.AccessPolicyApprovals, ctx.BashSecurityApprovals = a.access, a.security
		a.access, a.security = nil, nil
		a.dispatched = true
	}
}

func (s *llmRunStream) invokeAuthorizedHostBash(invocation *preparedToolInvocation) error {
	if request := s.prepareHostBashAuthorization(invocation); request != nil {
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
