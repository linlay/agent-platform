package plan

import (
	"strings"

	. "agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
)

func RegisterExactApproval(execCtx *ExecutionContext, fingerprint string) {
	if execCtx == nil || strings.TrimSpace(fingerprint) == "" {
		return
	}
	if execCtx.AccessPolicyApprovals == nil {
		execCtx.AccessPolicyApprovals = map[string]int{}
	}
	execCtx.AccessPolicyApprovals[fingerprint]++
}

func RegisterRuleApproval(execCtx *ExecutionContext, ruleKey string) {
	if execCtx == nil || !RuleReusable(ruleKey) {
		return
	}
	if execCtx.AccessPolicyRuleApprovals == nil {
		execCtx.AccessPolicyRuleApprovals = map[string]bool{}
	}
	execCtx.AccessPolicyRuleApprovals[ruleKey] = true
}

func ConsumeApproval(execCtx *ExecutionContext, plan BashPlan) bool {
	if plan.Blocked() {
		return false
	}
	if len(plan.Requirements) > 0 && !hasExactApproval(execCtx, plan) {
		if !HasApproval(execCtx, plan) {
			return false
		}
		for _, leaf := range plan.Requirements {
			if leaf.RequiresApproval() && !ConsumeApproval(execCtx, leaf) {
				return false
			}
		}
		return true
	}
	if execCtx == nil {
		return false
	}
	if RuleReusable(plan.RuleKey) && execCtx.AccessPolicyRuleApprovals != nil && execCtx.AccessPolicyRuleApprovals[plan.RuleKey] {
		return true
	}
	if len(execCtx.AccessPolicyApprovals) == 0 || strings.TrimSpace(plan.Fingerprint) == "" {
		return false
	}
	remaining := execCtx.AccessPolicyApprovals[plan.Fingerprint]
	if remaining <= 0 {
		return false
	}
	if remaining == 1 {
		delete(execCtx.AccessPolicyApprovals, plan.Fingerprint)
		return true
	}
	execCtx.AccessPolicyApprovals[plan.Fingerprint] = remaining - 1
	return true
}

func HasApproval(execCtx *ExecutionContext, plan BashPlan) bool {
	if plan.Blocked() {
		return false
	}
	if len(plan.Requirements) > 0 && !hasExactApproval(execCtx, plan) {
		for _, leaf := range plan.Requirements {
			if leaf.RequiresApproval() && !HasApproval(execCtx, leaf) {
				return false
			}
		}
		return true
	}
	if execCtx == nil {
		return false
	}
	if RuleReusable(plan.RuleKey) && execCtx.AccessPolicyRuleApprovals != nil && execCtx.AccessPolicyRuleApprovals[plan.RuleKey] {
		return true
	}
	return execCtx.AccessPolicyApprovals != nil && execCtx.AccessPolicyApprovals[plan.Fingerprint] > 0
}

func hasExactApproval(ctx *ExecutionContext, p BashPlan) bool {
	return ctx != nil && ctx.AccessPolicyApprovals[p.Fingerprint] > 0
}

func BashPlanMetadata(p BashPlan) map[string]any {
	out := map[string]any{"decision": string(p.Decision), "accessLevel": p.AccessLevel, "reason": p.Reason, "ruleKey": p.RuleKey}
	if p.Launch != nil {
		out["shell"] = map[string]any{"executable": p.Launch.Executable, "template": append([]string(nil), p.Launch.Template...), "dialect": string(p.Launch.Dialect), "syntaxSupported": p.Launch.SyntaxSupported}
	}
	if p.ScopeKind != "" {
		out["scopeKind"], out["scope"] = p.ScopeKind, p.Scope
	}
	if p.ContentSHA256 != "" {
		out["contentSHA256"] = p.ContentSHA256
	}
	if len(p.Requirements) > 0 {
		items := []any{}
		for _, leaf := range p.Requirements {
			items = append(items, BashPlanMetadata(leaf))
		}
		out["requirements"] = items
	}
	return out
}

func BashApprovalSource(ctx *ExecutionContext, p BashPlan) string {
	if pending := PendingBashPlan(ctx, p); pending.RequiresApproval() {
		p = pending
	}
	if !p.RequiresApproval() || !HasApproval(ctx, p) {
		return ""
	}
	for _, rule := range ApprovalRules(p) {
		if !ctx.AccessPolicyRuleApprovals[rule] {
			return "exact"
		}
	}
	return "run_rule"
}
