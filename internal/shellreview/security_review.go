package shellreview

import (
	bashsyntax "agent-platform/internal/shellreview/syntax/bash"
	"strings"
)

type syntaxReviewDecision string

const (
	ReviewAllow            syntaxReviewDecision = "allow"
	ReviewRequiresApproval syntaxReviewDecision = "requires_approval"
	ReviewBlock            syntaxReviewDecision = "block"
)

type syntaxReviewResult struct {
	Decision    syntaxReviewDecision
	Reason      string
	Fingerprint string
	RuleKey     string
	Level       int
}

func (r syntaxReviewResult) AutoApprovedAtLevel(level string) bool {
	return r.Decision == ReviewRequiresApproval && (level == "auto_approve" || level == "full_access")
}

const (
	RuleKeyTooComplex             = "bashast:too_complex"
	RuleKeyRuntimeWrapperXargs    = "bashsec:runtime_wrapper:xargs"
	RuleKeyRuntimeWrapperFindExec = "bashsec:runtime_wrapper:find_exec"

	LevelTooComplex     = 4
	LevelRuntimeWrapper = 3
)

// maxScriptDepth bounds recursive review of literal `bash -c` scripts.
const maxScriptDepth = 3

func reviewBashSecurity(command string) syntaxReviewResult {
	return reviewBashSecurityWithKnownVariables(command, nil)
}

// reviewBashSecurityWithKnownVariables reports shell constructs the reviewer
// cannot represent faithfully (hard block) or cannot analyze (approval). File,
// program and network effects belong to the access policy.
func reviewBashSecurityWithKnownVariables(command string, variables map[string]string) syntaxReviewResult {
	return reviewScript(command, command, variables, 0, false)
}

// findExecReviewed drops the find -exec requirement after the access review
// showed every find -exec read-only; dangerous inner builtins still block.
func reviewScript(original, command string, variables map[string]string, depth int, findExecReviewed bool) syntaxReviewResult {
	result := bashsyntax.ParseForSecurityWithKnownVariables(command, variables)
	switch result.Kind {
	case bashsyntax.Simple:
		return reviewFromAST(original, command, result, variables, depth, findExecReviewed)
	case bashsyntax.TooComplex:
		if bashsyntax.IsHardBlockReason(result.Reason) {
			return blockReview(result.Reason)
		}
		if blocked := reviewText(command, bashsyntax.ParseResult{}); blocked.Decision == ReviewBlock {
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
