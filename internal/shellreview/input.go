package shellreview

import (
	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/commandtext"
	. "agent-platform/internal/contracts"
	bashsyntax "agent-platform/internal/shellreview/syntax/bash"
	"strings"
)

// DefaultBashCwd is the working directory used when a Bash call names none.
// A read-only Workspace starts programs in the current Chat instead.
func DefaultBashCwd(session QuerySession) string {
	if SessionWorkspaceEditingDisabled(session) {
		return "@chat"
	}
	return "@workspace"
}

// rawTextRejection judges hidden characters by the grammar that executes the
// text: Bash (including direct connector argv) ignores quoted data, while an
// unsupported grammar cannot tell quoting apart, so every character counts.
func rawTextRejection(command string, unknownDialect bool) string {
	if unknownDialect {
		return commandtext.CheckAll(command)
	}
	return bashsyntax.CheckRawText(command)
}

func containsUnresolvedPlaceholder(value string) bool {
	return strings.Contains(value, bashsyntax.CommandSubstitutionPlaceholder) ||
		strings.Contains(value, bashsyntax.TrackedVariablePlaceholder)
}
