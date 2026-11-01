package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	. "agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
)

func decisionPriority(d Decision) int {
	switch d {
	case DecisionBlock:
		return 3
	case DecisionRequiresApproval:
		return 2
	case DecisionAutoApproved:
		return 1
	}
	return 0
}

// Combine deduplicates requirements and preserves the strongest decision.
func Combine(command, level string, plans []BashPlan) BashPlan {
	if len(plans) == 0 {
		return New(command, level, DecisionAllow, "", "", command)
	}
	unique := []BashPlan{}
	seen := map[string]bool{}
	for _, p := range plans {
		key := p.RuleKey + "\x00" + p.Fingerprint
		if !seen[key] {
			unique = append(unique, p)
			seen[key] = true
		}
	}
	if len(unique) == 1 {
		return unique[0]
	}
	chosen := unique[0]
	var reasons, keys []string
	for _, p := range unique {
		if decisionPriority(p.Decision) > decisionPriority(chosen.Decision) {
			chosen = p
		}
		if p.Reason != "" {
			reasons = append(reasons, p.Reason)
		}
		keys = append(keys, p.RuleKey+":"+p.Fingerprint+":"+string(p.Decision))
	}
	if chosen.Allowed() && !chosen.AutoApproved() {
		return chosen
	}
	sum := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
	return BashPlan{Decision: chosen.Decision, Reason: strings.Join(reasons, "; "), RuleKey: chosen.RuleKey + ":combined:" + hex.EncodeToString(sum[:8]), Fingerprint: hex.EncodeToString(sum[:]), CommandText: command, AccessLevel: level, Requirements: unique}
}

// ApprovalRules returns only requirements the user is being asked to approve.
func ApprovalRules(p BashPlan) []string {
	if p.Blocked() {
		return nil
	}
	if len(p.Requirements) == 0 {
		if p.RequiresApproval() {
			return []string{p.RuleKey}
		}
		return nil
	}
	var rules []string
	seen := map[string]bool{}
	for _, leaf := range p.Requirements {
		for _, rule := range ApprovalRules(leaf) {
			if !seen[rule] {
				rules = append(rules, rule)
				seen[rule] = true
			}
		}
	}
	return rules
}

// PendingBashPlan removes approved leaves but never a hard block. Re-evaluation
// uses the current file contents/environment rather than a cached allow result.
func PendingBashPlan(ctx *ExecutionContext, p BashPlan) BashPlan {
	if p.Blocked() || !p.RequiresApproval() || hasExactApproval(ctx, p) {
		return p
	}
	if len(p.Requirements) == 0 {
		return p
	}
	var leaves []BashPlan
	for _, leaf := range p.Requirements {
		if leaf.RequiresApproval() && HasApproval(ctx, leaf) {
			continue
		}
		leaves = append(leaves, leaf)
	}
	result := Combine(p.CommandText, p.AccessLevel, leaves)
	result.HasConnector, result.ReviewCommand, result.ConnectorOnly = p.HasConnector, p.ReviewCommand, p.ConnectorOnly
	result.ConnectorIDs = append([]string(nil), p.ConnectorIDs...)
	result.ConnectorCall = p.ConnectorCall
	result.UnknownDialect, result.LaunchFingerprint = p.UnknownDialect, p.LaunchFingerprint
	result.Launch = p.Launch
	return result
}
