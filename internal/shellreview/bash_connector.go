package shellreview

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	shellplan "agent-platform/internal/shellreview/plan"
	"agent-platform/internal/shellreview/types"
)

// connectorReviewCommand removes only verified CLI words from analysis. The
// original source is always executed; redirects, assignments, operators and
// nested shell commands remain subject to ordinary review.
func connectorReviewCommand(command string, words []types.WordSpan) string {
	sort.Slice(words, func(i, j int) bool { return words[i].Start < words[j].Start })
	var out strings.Builder
	end := 0
	for _, word := range words {
		if word.Expansion || word.Start < end || word.End > len(command) || word.Start >= word.End {
			continue
		}
		out.WriteString(command[end:word.Start])
		out.WriteString(":")
		end = word.End
	}
	out.WriteString(command[end:])
	return out.String()
}

// securityReview binds any residual shell approval to the original command,
// never to the neutral projection used to exclude mounted CLI payloads.
func securityReview(p shellplan.BashPlan, command string, variables map[string]string) syntaxReviewResult {
	if reason := rawTextRejection(command, p.UnknownDialect); reason != "" {
		return syntaxReviewResult{Decision: ReviewBlock, Reason: reason}
	}
	if p.UnknownDialect {
		// The whole-call access requirement carries unsupported grammar review.
		return syntaxReviewResult{Decision: ReviewAllow}
	}
	reviewed := p.ShellReviewCommand(command)
	result := reviewScript(reviewed, reviewed, variables, 0, p.FindExecReviewed)
	if result.Fingerprint != "" {
		result.Fingerprint = ApprovalFingerprint(command)
		if p.LaunchFingerprint != "" {
			sum := sha256.Sum256([]byte(result.Fingerprint + "\x00" + p.LaunchFingerprint))
			result.Fingerprint = hex.EncodeToString(sum[:])
		}
	}
	return result
}
