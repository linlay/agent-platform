package shellreview

import (
	"strings"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	shellplan "agent-platform/internal/shellreview/plan"
	bashsyntax "agent-platform/internal/shellreview/syntax/bash"
	"agent-platform/internal/shellreview/types"
)

func ReviewBashCommand(cfg config.AccessPolicyConfig, session QuerySession, command string, cwd string, variables map[string]string, contexts ...*ExecutionContext) shellplan.BashPlan {
	return reviewBashExecution(cfg, session, command, cwd, variables, nil, contexts...)
}

func ReviewBashCommandInEnvironment(cfg config.AccessPolicyConfig, session QuerySession, command, cwd string, variables map[string]string, environment *types.BashEnvironment, ctx *ExecutionContext) shellplan.BashPlan {
	return reviewBashExecution(cfg, session, command, cwd, variables, environment, ctx)
}

func containsUnresolvedPlaceholder(value string) bool {
	return strings.Contains(value, bashsyntax.CommandSubstitutionPlaceholder) ||
		strings.Contains(value, bashsyntax.TrackedVariablePlaceholder)
}
