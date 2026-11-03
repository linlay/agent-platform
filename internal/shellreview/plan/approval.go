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

// approvalConsumption computes the complete transaction before spending any
// exact grants. A missing later leaf cannot partially consume earlier leaves.
func approvalConsumption(ctx *ExecutionContext, p BashPlan) (map[string]int, bool) {
	if !p.RequiresApproval() || p.Blocked() {
		return nil, false
	}
	_, needed, ok := resolveApprovals(ctx, p)
	return needed, ok
}
func HasApproval(ctx *ExecutionContext, p BashPlan) bool {
	_, ok := approvalConsumption(ctx, p)
	return ok
}
func ConsumeApproval(ctx *ExecutionContext, p BashPlan) bool {
	needed, ok := approvalConsumption(ctx, p)
	if !ok {
		return false
	}
	for fingerprint, count := range needed {
		remaining := ctx.AccessPolicyApprovals[fingerprint] - count
		if remaining == 0 {
			delete(ctx.AccessPolicyApprovals, fingerprint)
		} else {
			ctx.AccessPolicyApprovals[fingerprint] = remaining
		}
	}
	return true
}

// ApprovalReservation can be returned only when launch is known not to have
// started. An ambiguous transport failure must never manufacture another grant.
type ApprovalReservation struct {
	ctx   *ExecutionContext
	exact map[string]int
	done  bool
}

func ReserveApproval(ctx *ExecutionContext, p BashPlan) (*ApprovalReservation, bool) {
	if !p.RequiresApproval() {
		return nil, p.Allowed()
	}
	needed, ok := approvalConsumption(ctx, p)
	if !ok {
		return nil, false
	}
	if !ConsumeApproval(ctx, p) {
		return nil, false
	}
	return &ApprovalReservation{ctx: ctx, exact: needed}, true
}
func (r *ApprovalReservation) Commit() {
	if r != nil {
		r.done = true
	}
}
func (r *ApprovalReservation) Restore() {
	if r == nil || r.done {
		return
	}
	r.done = true
	if r.ctx.AccessPolicyApprovals == nil {
		r.ctx.AccessPolicyApprovals = map[string]int{}
	}
	for fingerprint, count := range r.exact {
		r.ctx.AccessPolicyApprovals[fingerprint] += count
	}
}

// TransferExactApprovals moves only grants allocated to this invocation into
// its private context. Repeated preflight neither duplicates nor steals grants.
func TransferExactApprovals(ctx *ExecutionContext, p BashPlan, target map[string]int) {
	if ctx == nil || target == nil || p.Blocked() || !p.RequiresApproval() {
		return
	}
	combined := *ctx
	combined.AccessPolicyApprovals = map[string]int{}
	for key, count := range ctx.AccessPolicyApprovals {
		combined.AccessPolicyApprovals[key] = count
	}
	for key, count := range target {
		combined.AccessPolicyApprovals[key] += count
	}
	needed, _ := approvalConsumption(&combined, p)
	for key, count := range needed {
		count -= target[key]
		if count > ctx.AccessPolicyApprovals[key] {
			count = ctx.AccessPolicyApprovals[key]
		}
		if count <= 0 {
			continue
		}
		target[key] += count
		ctx.AccessPolicyApprovals[key] -= count
		if ctx.AccessPolicyApprovals[key] == 0 {
			delete(ctx.AccessPolicyApprovals, key)
		}
	}
}
