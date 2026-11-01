package plan

import (
	. "agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
)

// resolveApprovals uses a private ledger for display, validation and final
// consumption. Exact grants are allocated once, even across nested siblings.
func resolveApprovals(ctx *ExecutionContext, p BashPlan) (BashPlan, map[string]int, bool) {
	needed := map[string]int{}
	if p.Blocked() {
		return p, needed, false
	}
	if !p.RequiresApproval() {
		return p, needed, p.Allowed()
	}
	if ctx == nil {
		return p, needed, false
	}
	available := map[string]int{}
	for key, count := range ctx.AccessPolicyApprovals {
		available[key] = count
	}
	take := func(fingerprint string) bool {
		if available[fingerprint] <= 0 {
			return false
		}
		available[fingerprint]--
		needed[fingerprint]++
		return true
	}
	var visit func(BashPlan) (BashPlan, bool)
	visit = func(q BashPlan) (BashPlan, bool) {
		if q.Blocked() {
			return q, false
		}
		if !q.RequiresApproval() {
			return q, q.Allowed()
		}
		if RuleReusable(q.RuleKey) && ctx.AccessPolicyRuleApprovals[q.RuleKey] {
			return q, true
		}
		if take(q.Fingerprint) {
			return q, true
		}
		if len(q.Requirements) == 0 {
			return q, false
		}
		var missing []BashPlan
		satisfied := true
		for _, leaf := range q.Requirements {
			pending, ok := visit(leaf)
			if !ok {
				satisfied = false
			}
			if leaf.RequiresApproval() && ok {
				continue
			}
			missing = append(missing, pending)
		}
		if satisfied {
			return q, true
		}
		pending := Combine(q.CommandText, q.AccessLevel, missing)
		pending.HasConnector, pending.ReviewCommand, pending.ConnectorOnly = q.HasConnector, q.ReviewCommand, q.ConnectorOnly
		pending.ConnectorIDs = append([]string(nil), q.ConnectorIDs...)
		pending.ConnectorCall = q.ConnectorCall
		pending.UnknownDialect, pending.LaunchFingerprint, pending.Launch = q.UnknownDialect, q.LaunchFingerprint, q.Launch
		pending.UsesSSHAgent = q.UsesSSHAgent
		if pending.RequiresApproval() && pending.Fingerprint != q.Fingerprint && take(pending.Fingerprint) {
			return q, true
		}
		return pending, false
	}
	pending, satisfied := visit(p)
	if satisfied {
		return p, needed, true
	}
	return pending, needed, false
}
