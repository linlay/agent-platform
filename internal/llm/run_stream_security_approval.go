package llm

import (
	"strings"

	"agent-platform/internal/filetools"
	"agent-platform/internal/hitl"
	shellplan "agent-platform/internal/shellreview/plan"
	"agent-platform/internal/view"
)

func bashAccessInterceptResult(invocation *preparedToolInvocation, review shellplan.BashPlan) hitl.InterceptResult {
	command := strings.TrimSpace(review.CommandText)
	if command == "" && invocation != nil {
		command = strings.TrimSpace(mapStringArg(invocation.args, "command"))
	}
	ruleKey := strings.TrimSpace(review.RuleKey)
	if ruleKey == "" {
		ruleKey = "bash-access::" + review.Fingerprint
	}
	title, level := "Bash access approval", 1
	if review.Syntax {
		title = "Bash security approval"
		if review.SyntaxLevel > 0 {
			level = review.SyntaxLevel
		}
	}
	return hitl.InterceptResult{
		Intercepted: true,
		Rule: hitl.FlatRule{
			RuleKey: ruleKey,
			Level:   level,
			Title:   title,
			View:    view.Builtin("approval"),
		},
		OriginalCommand: command,
		MatchedCommand:  command,
		MatchedWhole:    true,
	}
}

func fileWriteInterceptResult(plan filetools.WritePlan) hitl.InterceptResult {
	title := "File write approval"
	if strings.EqualFold(strings.TrimSpace(plan.Operation), "edit") || strings.EqualFold(strings.TrimSpace(plan.ToolName), "file_edit") {
		title = "File edit approval"
	}
	return hitl.InterceptResult{
		Intercepted: true,
		Rule: hitl.FlatRule{
			RuleKey: plan.RuleKey,
			Level:   2,
			Title:   title,
			View:    view.Builtin("approval"),
		},
		OriginalCommand: plan.CommandText,
		MatchedCommand:  plan.CommandText,
		MatchedWhole:    true,
	}
}

func fileAccessInterceptResult(plan filetools.AccessPlan) hitl.InterceptResult {
	title := "File read approval"
	if plan.Mode == filetools.WriteAccess {
		title = "File path approval"
	}
	return hitl.InterceptResult{
		Intercepted: true,
		Rule: hitl.FlatRule{
			RuleKey: plan.RuleKey,
			Level:   1,
			Title:   title,
			View:    view.Builtin("approval"),
		},
		OriginalCommand: plan.CommandText,
		MatchedCommand:  plan.CommandText,
		MatchedWhole:    true,
	}
}
