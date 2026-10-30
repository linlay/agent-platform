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

// ConnectorCall is one plain call of a mounted connector's CLI. Prefix holds
// the words that start the entry as written (the program, or an interpreter
// and its script); Argv holds the arguments after it.
type ConnectorCall struct {
	ConnectorID string
	Prefix      []string
	Argv        []string
}

// DirectConnectorInvocation permits credential injection only into one verified
// child. Shell syntax/wrappers are never executed with connector credentials.
func DirectConnectorInvocation(session QuerySession, command, cwd string, vars map[string]string) *ConnectorInvocation {
	if session.AgentHasRuntimeSandbox || len(session.ConnectorCLIEntries) == 0 {
		return nil
	}
	cmd, ok := singleCall(command, vars)
	if !ok {
		return nil
	}
	x := analyzeBashExecution(session, cmd, cwd, vars, nil)
	if !x.Connector || x.Wrapped || len(x.Argv) == 0 {
		return nil
	}
	var err error
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

// singleCall accepts one simple command without shell operators, background,
// negation, redirects, assignments or expansions.
func singleCall(command string, vars map[string]string) (bashast.SimpleCommand, bool) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return bashast.SimpleCommand{}, false
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Negated || len(stmt.Redirs) > 0 || len(call.Assigns) > 0 {
		return bashast.SimpleCommand{}, false
	}
	p := bashast.ParseForExecution(command, vars)
	if p.Kind != bashast.Simple || len(p.Commands) != 1 {
		return bashast.SimpleCommand{}, false
	}
	cmd := p.Commands[0]
	for _, word := range cmd.Words {
		if word.Expansion {
			return bashast.SimpleCommand{}, false
		}
	}
	return cmd, true
}

// connectorCall reports the whole command as one plain connector call. An
// interpreter-run entry keeps only the arguments after its script.
func connectorCall(command string, vars map[string]string, x BashExecution) *ConnectorCall {
	if !x.Connector || x.Wrapped || x.Uncertain || len(x.Argv) == 0 {
		return nil
	}
	if _, ok := singleCall(command, vars); !ok {
		return nil
	}
	split := 1
	if x.TrustedInterpreter {
		// x.Script may be canonicalized; find the script word as written.
		script := interpreterScript(commandFamily(x.Argv[0]), x.Argv[1:])
		split = 0
		for i, token := range x.Argv[1:] {
			if script != "" && token == script {
				split = i + 2
				break
			}
		}
		if split == 0 {
			return nil
		}
	}
	return &ConnectorCall{ConnectorID: x.ConnectorID, Prefix: append([]string{}, x.Argv[:split]...), Argv: append([]string{}, x.Argv[split:]...)}
}
