package shellreview

import (
	"path/filepath"
	"strings"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
	shellplan "agent-platform/internal/shellreview/plan"
	bashsyntax "agent-platform/internal/shellreview/syntax/bash"
	"agent-platform/internal/shellreview/types"
)

type redirectAccessKind int

const (
	redirectAccessNeutral redirectAccessKind = iota
	redirectAccessRead
	redirectAccessWrite
	redirectAccessUnknown
)

func ReviewBashCommand(cfg config.AccessPolicyConfig, session QuerySession, command string, cwd string, variables map[string]string, contexts ...*ExecutionContext) shellplan.BashPlan {
	return reviewBashExecution(cfg, session, command, cwd, variables, nil, contexts...)
}

func ReviewBashCommandInEnvironment(cfg config.AccessPolicyConfig, session QuerySession, command, cwd string, variables map[string]string, environment *types.BashEnvironment, ctx *ExecutionContext) shellplan.BashPlan {
	return reviewBashExecution(cfg, session, command, cwd, variables, environment, ctx)
}

func classifyRedirectAccess(redirect types.Redirect) redirectAccessKind {
	if redirect.IsHeredoc {
		return redirectAccessNeutral
	}
	op := strings.TrimSpace(redirect.Op)
	target := strings.TrimSpace(redirect.Target)
	if op == "" {
		return redirectAccessUnknown
	}
	if op == "<<<" {
		return redirectAccessNeutral
	}
	if isDevNullRedirectTarget(target) {
		return redirectAccessNeutral
	}
	switch op {
	case "<&", ">&":
		if isFileDescriptorRedirectTarget(target) {
			return redirectAccessNeutral
		}
		return redirectAccessUnknown
	case "<":
		return redirectAccessRead
	case ">", ">>", ">|", ">>|", "&>", "&>|", "&>>", "&>>|":
		return redirectAccessWrite
	case "<>":
		return redirectAccessWrite
	default:
		if strings.Contains(op, ">") {
			return redirectAccessWrite
		}
		if strings.Contains(op, "<") {
			return redirectAccessRead
		}
		return redirectAccessUnknown
	}
}

func isDevNullRedirectTarget(target string) bool {
	return filepath.ToSlash(filepath.Clean(pathutil.ExpandHome(strings.TrimSpace(target)))) == "/dev/null"
}

func isFileDescriptorRedirectTarget(target string) bool {
	target = strings.TrimSpace(target)
	if target == "-" {
		return true
	}
	if target == "" {
		return false
	}
	for _, r := range target {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func containsUnresolvedPlaceholder(value string) bool {
	return strings.Contains(value, bashsyntax.CommandSubstitutionPlaceholder) ||
		strings.Contains(value, bashsyntax.TrackedVariablePlaceholder)
}
