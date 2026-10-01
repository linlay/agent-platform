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

func ApprovalFingerprint(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:])
}
