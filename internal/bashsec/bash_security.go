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

const (
	RuleKeyRedirections               = "bashsec:redirections"
	RuleKeyQuotedNewline              = "bashsec:quoted_newline"
	RuleKeyObfuscatedFlagsTripleQuote = "bashsec:obfuscated_flags:triple_quote"
	RuleKeyTooComplex                 = "bashast:too_complex"
	RuleKeyRuntimeWrapperXargs        = "bashsec:runtime_wrapper:xargs"
	RuleKeyRuntimeWrapperFindExec     = "bashsec:runtime_wrapper:find_exec"

	LevelRedirections               = 2
	LevelQuotedNewline              = 2
	LevelObfuscatedFlagsTripleQuote = 3
	LevelTooComplex                 = 4
	LevelRuntimeWrapper             = 3
)

func ReviewBashSecurity(command string) ReviewResult {
	return ReviewBashSecurityWithKnownVariables(command, nil)
}

func ReviewBashSecurityWithKnownVariables(command string, variables map[string]string) ReviewResult {
	astResult, embeddedScripts := bashast.ParseWithEmbeddedDetectionAndKnownVariables(command, variables)
	switch astResult.Kind {
	case bashast.Simple:
		return reviewFromAST(command, astResult, embeddedScripts)
	case bashast.TooComplex:
		if bashast.IsHardBlockReason(astResult.Reason) {
			return blockReview(astResult.Reason)
		}
		legacy := reviewBashSecurityLegacy(command)
		if legacy.Decision != ReviewAllow {
			return legacy
		}
		reason := strings.TrimSpace(astResult.Reason)
		if reason == "" {
			reason = "Command is too complex for static AST security analysis"
		}
		return ReviewResult{
			Decision:    ReviewRequiresApproval,
			Reason:      reason,
			Fingerprint: ApprovalFingerprint(command),
			RuleKey:     RuleKeyTooComplex,
			Level:       LevelTooComplex,
		}
	case bashast.ParseUnavailable:
		return reviewBashSecurityLegacy(command)
	default:
		return ReviewResult{
			Decision:    ReviewRequiresApproval,
			Reason:      "Command could not be classified by AST security analysis",
			Fingerprint: ApprovalFingerprint(command),
			RuleKey:     RuleKeyTooComplex,
			Level:       LevelTooComplex,
		}
	}
}
