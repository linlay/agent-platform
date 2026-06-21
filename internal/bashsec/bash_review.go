package bashsec

import (
	"crypto/sha256"
	"encoding/hex"
)

func blockReview(reason string) ReviewResult {
	return ReviewResult{Decision: ReviewBlock, Reason: reason}
}

func approvalReview(command, reason, ruleKey string, level int) ReviewResult {
	return ReviewResult{
		Decision:    ReviewRequiresApproval,
		Reason:      reason,
		Fingerprint: ApprovalFingerprint(command),
		RuleKey:     ruleKey,
		Level:       level,
	}
}

func approvalReviewForValidator(command, reason, validatorName string) ReviewResult {
	switch validatorName {
	case "redirections":
		return approvalReview(command, reason, RuleKeyRedirections, LevelRedirections)
	case "quoted_newline":
		return approvalReview(command, reason, RuleKeyQuotedNewline, LevelQuotedNewline)
	case "obfuscated_flags":
		return approvalReview(command, reason, RuleKeyObfuscatedFlagsTripleQuote, LevelObfuscatedFlagsTripleQuote)
	default:
		return approvalReview(command, reason, RuleKeyTooComplex, LevelTooComplex)
	}
}

func ApprovalFingerprint(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:])
}

func canApproveBashSecurityFailure(name, command, baseCommand, fullyUnquotedContent, reason string) bool {
	switch name {
	case "redirections":
		return reason == outputRedirectionReason &&
			hasOutputRedirection(fullyUnquotedContent) &&
			!hasInputRedirection(fullyUnquotedContent)
	case "quoted_newline":
		return hasOutputRedirection(fullyUnquotedContent) &&
			!hasInputRedirection(fullyUnquotedContent)
	case "obfuscated_flags":
		return reason == tripleQuoteReason && isPythonInlineCommandWithTripleQuotes(command, baseCommand)
	default:
		return false
	}
}
