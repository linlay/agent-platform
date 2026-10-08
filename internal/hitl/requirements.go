package hitl

import (
	"agent-platform/internal/view"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// CombineRequirements freezes all matching confirmations into one request.
// Multiple command-rewriting forms cannot safely share one shell invocation.
func CombineRequirements(command string, matches []InterceptResult) InterceptResult {
	if len(matches) == 0 {
		return InterceptResult{}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	result := InterceptResult{Intercepted: true, OriginalCommand: command, MatchedCommand: command, MatchedWhole: true, Requirements: matches}
	result.Rule = FlatRule{Mode: "approval", View: view.Builtin("confirm_dialog")}
	keys := []string{command}
	var titles []string
	for _, match := range matches {
		keys = append(keys, match.Rule.SourcePath, match.Rule.RuleKey)
		title := match.Rule.Title
		if title == "" {
			title = match.Rule.Command + " " + match.Rule.Match
		}
		titles = append(titles, title)
		if match.Rule.Level > result.Rule.Level {
			result.Rule.Level = match.Rule.Level
		}
		if match.Rule.Timeout > result.Rule.Timeout {
			result.Rule.Timeout = match.Rule.Timeout
		}
		if !match.Rule.IsBuiltinApproval() {
			result.Conflict = "Multiple matching hooks include a form. Split the operation or disambiguate the form rules before execution."
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
	result.Rule.RuleKey = "hitl:requirements:" + hex.EncodeToString(sum[:])
	result.Rule.Title = strings.Join(titles, "; ")
	return result
}

func RequirementMetadata(match InterceptResult) []any {
	matches := match.Requirements
	if len(matches) == 0 && match.Intercepted {
		matches = []InterceptResult{match}
	}
	items := make([]any, 0, len(matches))
	for _, m := range matches {
		items = append(items, map[string]any{"ruleKey": m.Rule.RuleKey, "reason": m.Rule.Title, "command": m.MatchedCommand, "mode": m.Rule.EffectiveMode()})
	}
	return items
}
