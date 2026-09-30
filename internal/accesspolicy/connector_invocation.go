package accesspolicy

import (
	"agent-platform/internal/bashast"
	. "agent-platform/internal/contracts"
	"mvdan.cc/sh/v3/syntax"
	"strings"
)

type ConnectorInvocation struct {
	ID, Program string
	Args        []string
}

// DirectConnectorInvocation permits credential injection only into one verified
// child. Shell syntax/wrappers are never executed with connector credentials.
func DirectConnectorInvocation(session QuerySession, command, cwd string, vars map[string]string) *ConnectorInvocation {
	if session.AgentHasRuntimeSandbox {
		return nil
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return nil
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Negated || len(stmt.Redirs) > 0 || len(call.Assigns) > 0 {
		return nil
	}
	p := bashast.ParseForExecution(command, vars)
	if p.Kind != bashast.Simple || len(p.Commands) != 1 {
		return nil
	}
	cmd := p.Commands[0]
	for _, word := range cmd.Words {
		if word.Expansion {
			return nil
		}
	}
	x := analyzeBashExecution(session, cmd, cwd, vars, nil)
	if !x.Connector || x.Wrapped || len(x.Argv) == 0 {
		return nil
	}
	target := x.Program
	if x.TrustedInterpreter {
		target = x.Script
	}
	if target == "" {
		target = resolvedProgram(x.Argv[0], cwd, vars)
	}
	target, err = NormalizePath(resolveAgainstCwd(target, cwd))
	if err != nil {
		return nil
	}
	program := resolvedProgram(x.Argv[0], cwd, vars)
	if program == "" {
		return nil
	}
	for _, entry := range session.ConnectorCLIEntries {
		if same, err := NormalizePath(entry.Path); err == nil && same == target {
			return &ConnectorInvocation{ID: entry.ConnectorID, Program: program, Args: append([]string(nil), x.Argv[1:]...)}
		}
	}
	return nil
}
