package llm

import (
	"agent-platform/internal/view"
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/bashsec"
	"agent-platform/internal/filetools"
	"agent-platform/internal/hitl"
)

func (s *llmRunStream) registerBashSecurityApproval(fingerprint string) {
	if s.execCtx == nil || strings.TrimSpace(fingerprint) == "" {
		return
	}
	if s.execCtx.BashSecurityApprovals == nil {
		s.execCtx.BashSecurityApprovals = map[string]int{}
	}
	s.execCtx.BashSecurityApprovals[fingerprint]++
}

func (s *llmRunStream) hasBashSecurityApproval(fingerprint string) bool {
	if s == nil || s.execCtx == nil || strings.TrimSpace(fingerprint) == "" || len(s.execCtx.BashSecurityApprovals) == 0 {
		return false
	}
	return s.execCtx.BashSecurityApprovals[fingerprint] > 0
}

func (s *llmRunStream) shouldAutoApproveBashSecurity(review bashsec.ReviewResult) bool {
	if s == nil || s.execCtx == nil || review.Level <= 0 {
		return false
	}
	return review.AutoApprovedAtLevel(s.execCtx.Session.AccessLevel)
}

func (s *llmRunStream) isSandboxRuntime() bool {
	if s == nil {
		return false
	}
	return s.session.AgentHasRuntimeSandbox || (s.execCtx != nil && s.execCtx.Session.AgentHasRuntimeSandbox)
}

func bashSecurityInterceptResult(invocation *preparedToolInvocation, review bashsec.ReviewResult) hitl.InterceptResult {
	command := ""
	if invocation != nil {
		command = strings.TrimSpace(mapStringArg(invocation.args, "command"))
	}
	ruleKey := strings.TrimSpace(review.RuleKey)
	if ruleKey == "" {
		ruleKey = "bash-security::" + review.Fingerprint
	}
	level := review.Level
	if level <= 0 {
		level = 1
	}
	return hitl.InterceptResult{
		Intercepted: true,
		Rule: hitl.FlatRule{
			RuleKey: ruleKey,
			Level:   level,
			Title:   "Bash security approval",
			View:    view.Builtin("approval"),
		},
		OriginalCommand: command,
		MatchedCommand:  command,
		MatchedWhole:    true,
	}
}

func bashAccessInterceptResult(invocation *preparedToolInvocation, review accesspolicy.BashPlan) hitl.InterceptResult {
	command := strings.TrimSpace(review.CommandText)
	if command == "" && invocation != nil {
		command = strings.TrimSpace(mapStringArg(invocation.args, "command"))
	}
	ruleKey := strings.TrimSpace(review.RuleKey)
	if ruleKey == "" {
		ruleKey = "bash-access::" + review.Fingerprint
	}
	return hitl.InterceptResult{
		Intercepted: true,
		Rule: hitl.FlatRule{
			RuleKey: ruleKey,
			Level:   1,
			Title:   "Bash access approval",
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
