package shellreview

import (
	"crypto/sha256"
	"encoding/hex"
)

func blockReview(reason string) syntaxReviewResult {
	return syntaxReviewResult{Decision: ReviewBlock, Reason: reason}
}

func approvalReview(command, reason, ruleKey string, level int) syntaxReviewResult {
	return syntaxReviewResult{
		Decision:    ReviewRequiresApproval,
		Reason:      reason,
		Fingerprint: ApprovalFingerprint(command),
		RuleKey:     ruleKey,
		Level:       level,
	}
}

func ApprovalFingerprint(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:])
}
