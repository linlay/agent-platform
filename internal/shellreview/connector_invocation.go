package shellreview

import (
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/identity"
	bashsyntax "agent-platform/internal/shellreview/syntax/bash"
	"agent-platform/internal/shellreview/types"

	"mvdan.cc/sh/v3/syntax"
)

// DirectConnectorInvocation permits credential injection only into one verified
// child. Shell syntax/wrappers are never executed with connector credentials.
func DirectConnectorInvocation(session QuerySession, command, cwd string, vars map[string]string) *types.ConnectorInvocation {
	if session.AgentHasRuntimeSandbox || len(session.ConnectorCLIEntries) == 0 {
		return nil
	}
	cmd, ok := singleCall(command, vars)
	if !ok {
		return nil
	}
	x := analyzeBashExecution(session, cmd, cwd, vars, nil)
	return identity.VerifiedConnectorInvocation(session, x, cwd, vars)
}

// singleCall accepts one simple command without shell operators, background,
// negation, redirects, assignments or expansions.
func singleCall(command string, vars map[string]string) (types.Invocation, bool) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return types.Invocation{}, false
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Negated || len(stmt.Redirs) > 0 || len(call.Assigns) > 0 {
		return types.Invocation{}, false
	}
	p := bashsyntax.ParseForExecution(command, vars)
	if p.Kind != bashsyntax.Simple || len(p.Commands) != 1 {
		return types.Invocation{}, false
	}
	cmd := p.Commands[0]
	for _, word := range cmd.Words {
		if word.Expansion {
			return types.Invocation{}, false
		}
	}
	return cmd, true
}

// connectorCall reports the whole command as one plain connector call. An
// interpreter-run entry keeps only the arguments after its script.
func connectorCall(command string, vars map[string]string, x types.BashExecution) *types.ConnectorCall {
	if !x.Connector || x.Wrapped || x.Uncertain || len(x.Argv) == 0 {
		return nil
	}
	if _, ok := singleCall(command, vars); !ok {
		return nil
	}
	return identity.VerifiedConnectorCall(x)
}
