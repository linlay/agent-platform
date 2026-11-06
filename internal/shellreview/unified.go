package shellreview

import (
	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/contracts"
	shellplan "agent-platform/internal/shellreview/plan"
	"agent-platform/internal/shellreview/resources"
	"agent-platform/internal/shellreview/types"
)

// includeSyntaxRequirements is the sole join between syntax safety facts and
// resource policy. Both now use the same invocation-bound approval object.
func includeSyntaxRequirements(p shellplan.BashPlan, session contracts.QuerySession, command, cwd string, vars map[string]string, environment *types.BashEnvironment) shellplan.BashPlan {
	review := securityReview(p, command, vars)
	if review.Decision == ReviewAllow {
		return p
	}
	level := accesspolicy.SessionAccessLevel(session)
	decision := accesspolicy.DecisionRequiresApproval
	if review.Decision == ReviewBlock {
		decision = accesspolicy.DecisionBlock
	} else if review.AutoApprovedAtLevel(level) {
		decision = accesspolicy.DecisionAutoApproved
	}
	rule := review.RuleKey
	if rule == "" {
		rule = "bash-security:block"
	}
	syntax := shellplan.New(command, level, decision, review.Reason, rule, command)
	syntax.Syntax = true
	syntax.SyntaxLevel = review.Level
	actual := cwd
	if actual == "" {
		actual = DefaultBashCwd(session)
	}
	if resolved, err := accesspolicy.ResolveSessionPath(session, actual); err == nil {
		actual = resolved
	}
	if session.AgentHasRuntimeSandbox {
		if environment != nil && environment.Directory != nil {
			if canonical, err := environment.Directory(actual); err == nil {
				actual = canonical
			}
		}
	} else if normalized, err := accesspolicy.NormalizePath(actual); err == nil {
		actual = normalized
	}
	// The effective environment and canonical cwd bind even an unparsed guest
	// invocation. Launch binding is added to the entire combined plan afterward.
	version := resources.ExecutionFingerprint(session.AgentHasRuntimeSandbox, resources.ExecutionVersion{Fingerprint: syntax.Fingerprint, RuleKey: syntax.RuleKey, AccessLevel: level}, types.BashExecution{Cwd: actual, Identity: "shell-syntax", Env: vars}, vars, nil)
	syntax.Fingerprint = version.Fingerprint
	result := shellplan.Combine(command, level, []shellplan.BashPlan{syntax, p})
	result.ReviewCommand, result.HasConnector, result.ConnectorOnly = p.ReviewCommand, p.HasConnector, p.ConnectorOnly
	result.ConnectorIDs, result.ConnectorCall = p.ConnectorIDs, p.ConnectorCall
	result.UnknownDialect, result.UsesSSHAgent = p.UnknownDialect, p.UsesSSHAgent
	result.FindExecReviewed = p.FindExecReviewed
	return result
}
