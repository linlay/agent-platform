package llm

import (
	"strings"

	"agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/hitl"
)

func (s *llmRunStream) syncAccessLevelFromRunControl() bool {
	if s == nil || s.runControl == nil {
		return false
	}
	accessLevel, version := s.runControl.AccessLevelSnapshot()
	normalized, ok := NormalizeAccessLevel(accessLevel)
	if !ok {
		normalized = AccessLevelDefault
	}
	if version == s.accessLevelVersion && strings.EqualFold(strings.TrimSpace(s.session.AccessLevel), normalized) {
		return false
	}
	s.accessLevelVersion = version
	s.session.AccessLevel = normalized
	if s.execCtx != nil {
		s.execCtx.AccessLevel = normalized
		s.execCtx.Session.AccessLevel = normalized
	}
	return true
}

func (s *llmRunStream) refreshAccessLevelForInvocation(invocation *preparedToolInvocation) bool {
	changed := s.syncAccessLevelFromRunControl()
	if changed {
		invalidateAccessPolicyPlans(invocation)
	}
	return changed
}

func invalidateAccessPolicyPlans(invocation *preparedToolInvocation) {
	if invocation == nil {
		return
	}
	invocation.bashAccessReview = nil
	invocation.fileAccessPlan = nil
	invocation.fileWritePlan = nil
}

func isAccessPolicyApprovalMatch(result hitl.InterceptResult) bool {
	ruleKey := strings.ToLower(strings.TrimSpace(result.Rule.RuleKey))
	return strings.HasPrefix(ruleKey, "file-") || strings.HasPrefix(ruleKey, "bash-access")
}

func (s *llmRunStream) invocationNeedsAccessPolicyApproval(invocation *preparedToolInvocation) bool {
	if invocation == nil {
		return false
	}
	if accessPlan := s.lookupFileAccessPlan(invocation); accessPlan != nil && s.fileAccessPlanNeedsApproval(*accessPlan) {
		return true
	}
	if plan := s.lookupFileWritePlan(invocation); plan != nil && s.fileWritePlanNeedsApproval(*plan) && !filetools.HasWriteApproval(s.execCtx, *plan) {
		return true
	}
	if review := s.lookupBashAccessReview(invocation); review.Decision == accesspolicy.DecisionRequiresApproval && !accesspolicy.HasApproval(s.execCtx, review) {
		return true
	}
	return false
}

func (s *llmRunStream) accessLevelAutoApprovalAnswer(awaitingID string, invocation *preparedToolInvocation, match hitl.InterceptResult, accessLevel string) map[string]any {
	command := accessLevelApprovalCommand(s, invocation, match)
	return map[string]any{
		"mode":   "approval",
		"status": "answered",
		"approvals": []any{
			map[string]any{
				"id":       approvalAnswerID(awaitingID, invocation),
				"command":  command,
				"decision": "auto_approved",
				"reason":   "accessLevel=" + accessLevel,
			},
		},
	}
}

func accessLevelApprovalCommand(s *llmRunStream, invocation *preparedToolInvocation, match hitl.InterceptResult) string {
	if command := strings.TrimSpace(match.MatchedCommand); command != "" {
		return command
	}
	if command := strings.TrimSpace(match.OriginalCommand); command != "" {
		return command
	}
	if s != nil && invocation != nil {
		return AnyStringNode(s.buildApprovalAskItem(invocation)["command"])
	}
	return ""
}

func accessLevelAutoApprovalBatchAnswer(batch *pendingHITLApprovalBatch, entries []map[string]any) map[string]any {
	return map[string]any{
		"mode":      "approval",
		"status":    "answered",
		"approvals": entries,
	}
}

func approvalAnswerID(awaitingID string, invocation *preparedToolInvocation) string {
	if invocation != nil && strings.TrimSpace(invocation.toolID) != "" {
		return strings.TrimSpace(invocation.toolID)
	}
	return strings.TrimSpace(awaitingID)
}
