package bashsec

import (
	"strings"

	"agent-platform/internal/bashast"
)

type ReviewDecision string

const (
	ReviewAllow            ReviewDecision = "allow"
	ReviewRequiresApproval ReviewDecision = "requires_approval"
	ReviewBlock            ReviewDecision = "block"
)

type ReviewResult struct {
	Decision    ReviewDecision
	Reason      string
	Fingerprint string
	RuleKey     string
	Level       int
}

func (r ReviewResult) AutoApprovedAtLevel(level string) bool {
	return r.Decision == ReviewRequiresApproval && (level == "auto_approve" || level == "full_access")
}

const (
	// RuleKeyRedirections is retained for sandbox override configuration; output
	// redirection targets are reviewed by the access policy, not by bashsec.
	RuleKeyRedirections           = "bashsec:redirections"
	RuleKeyTooComplex             = "bashast:too_complex"
	RuleKeyRuntimeWrapperXargs    = "bashsec:runtime_wrapper:xargs"
	RuleKeyRuntimeWrapperFindExec = "bashsec:runtime_wrapper:find_exec"

	LevelTooComplex     = 4
	LevelRuntimeWrapper = 3
)

// maxScriptDepth bounds recursive review of literal `bash -c` scripts.
const maxScriptDepth = 3

func ReviewBashSecurity(command string) ReviewResult {
	return ReviewBashSecurityWithKnownVariables(command, nil)
}

// ReviewBashSecurityWithKnownVariables reports shell constructs the reviewer
// cannot represent faithfully (hard block) or cannot analyze (approval). File,
// program and network effects belong to the access policy.
func ReviewBashSecurityWithKnownVariables(command string, variables map[string]string) ReviewResult {
	return reviewScript(command, command, variables, 0)
}

func reviewScript(original, command string, variables map[string]string, depth int) ReviewResult {
	result := bashast.ParseForSecurityWithKnownVariables(command, variables)
	switch result.Kind {
	case bashast.Simple:
		return reviewFromAST(original, command, result, variables, depth)
	case bashast.TooComplex:
		if bashast.IsHardBlockReason(result.Reason) {
			return blockReview(result.Reason)
		}
		if blocked := reviewText(command, bashast.ParseResult{}); blocked.Decision == ReviewBlock {
			return blocked
		}
		reason := strings.TrimSpace(result.Reason)
		if reason == "" {
			reason = "Command is too complex for static AST security analysis"
		}
		return approvalReview(original, reason, RuleKeyTooComplex, LevelTooComplex)
	default:
		return approvalReview(original, "Command could not be classified by AST security analysis", RuleKeyTooComplex, LevelTooComplex)
	}
}
