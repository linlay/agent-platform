package llm

import (
	"strings"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/bashast"
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
	return s.execCtx.HITLLevel >= review.Level
}

const sandboxBashSecurityOverrideReason = "sandbox-bash.security.bashsec-overrides"

func (s *llmRunStream) isSandboxRuntime() bool {
	if s == nil {
		return false
	}
	return s.session.AgentHasRuntimeSandbox || (s.execCtx != nil && s.execCtx.Session.AgentHasRuntimeSandbox)
}

func (s *llmRunStream) sandboxBashSecurityOverrideAction(invocation *preparedToolInvocation, review bashsec.ReviewResult) string {
	if s == nil || s.engine == nil || invocation == nil || !s.isSandboxRuntime() {
		return ""
	}
	if review.Decision != bashsec.ReviewRequiresApproval || review.RuleKey != bashsec.RuleKeyRedirections {
		return ""
	}
	overrides := s.engine.cfg.SandboxBash.Security.BashsecOverrides
	command := strings.TrimSpace(mapStringArg(invocation.args, "command"))
	if sandboxBashHasHeredocOutputRedirection(command, s.execCtxRuntimeEnvOverrides()) {
		if action := strings.TrimSpace(overrides.HeredocOutputRedirection); action != "" {
			return action
		}
	}
	return strings.TrimSpace(overrides.OutputRedirection)
}

func (s *llmRunStream) executeSandboxBashSecurityOverride(invocation *preparedToolInvocation, review bashsec.ReviewResult) (bool, error) {
	switch strings.TrimSpace(s.sandboxBashSecurityOverrideAction(invocation, review)) {
	case "allow":
		return true, s.executeOriginalBash(invocation)
	case "auto":
		if s.engine != nil && s.engine.cfg.SandboxBash.Security.AuditAutoApprovals {
			s.applyHITLDecision(invocation, bashSecurityInterceptResult(invocation, review), "", "auto_approved", sandboxBashSecurityOverrideReason, true)
		}
		return true, s.executeOriginalBash(invocation)
	case "block":
		s.appendOriginalToolResult(invocation, bashSecurityBlockedToolResult(review))
		return true, nil
	default:
		return false, nil
	}
}

func (s *llmRunStream) execCtxRuntimeEnvOverrides() map[string]string {
	if s == nil || s.execCtx == nil {
		return nil
	}
	return s.execCtx.RuntimeEnvOverrides
}

func sandboxBashHasHeredocOutputRedirection(command string, variables map[string]string) bool {
	result := bashast.ParseForSecurityWithKnownVariables(command, variables)
	if result.Kind != bashast.Simple {
		return false
	}
	for _, cmd := range result.Commands {
		hasHeredoc := false
		hasOutput := false
		for _, redirect := range cmd.Redirects {
			if redirect.IsHeredoc {
				hasHeredoc = true
				continue
			}
			if bashSecurityRedirectIsOutput(redirect.Op) {
				hasOutput = true
			}
		}
		if hasHeredoc && hasOutput {
			return true
		}
	}
	return false
}

func bashSecurityRedirectIsOutput(op string) bool {
	switch strings.TrimSpace(op) {
	case ">", ">>", ">|", ">>|", ">&", "&>", "&>|", "&>>", "&>>|":
		return true
	default:
		return strings.Contains(op, ">")
	}
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
			RuleKey:      ruleKey,
			Level:        level,
			Title:        "Bash security approval",
			ViewportType: "builtin",
			ViewportKey:  "approval",
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
			RuleKey:      ruleKey,
			Level:        1,
			Title:        "Bash access approval",
			ViewportType: "builtin",
			ViewportKey:  "approval",
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
			RuleKey:      plan.RuleKey,
			Level:        2,
			Title:        title,
			ViewportType: "builtin",
			ViewportKey:  "approval",
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
			RuleKey:      plan.RuleKey,
			Level:        1,
			Title:        title,
			ViewportType: "builtin",
			ViewportKey:  "approval",
		},
		OriginalCommand: plan.CommandText,
		MatchedCommand:  plan.CommandText,
		MatchedWhole:    true,
	}
}
