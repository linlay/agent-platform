package shellreview

import (
	"errors"
	"strings"

	. "agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/shellreview/identity"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
	"agent-platform/internal/shellreview/unwrap"
)

func analyzeBashExecution(session QuerySession, cmd types.Invocation, cwd string, variables map[string]string, env *types.BashEnvironment) (x types.BashExecution) {
	x = types.BashExecution{Argv: cmd.Argv, Words: types.SourceWords(cmd), Cwd: cwd, Uncertain: cmd.Uncertain}
	allowBuiltins := true
	vars := CloneStringMap(variables)
	if vars == nil {
		vars = map[string]string{}
	}
	for key, value := range cmd.Variables {
		vars[key] = value
	}
	for _, assignment := range cmd.EnvVars {
		vars[assignment.Name] = assignment.Value
	}
	defer func() {
		x.Env = vars
		x.ConnectorID = identity.ConnectorExecution(session, x, vars, env)
		x.Connector = x.ConnectorID != ""
	}()
	for depth := 0; depth < 8 && len(x.Argv) > 0; depth++ {
		name := x.Argv[0]
		base := identity.CommandFamily(name)
		if containsUnresolvedPlaceholder(name) {
			x.Uncertain = true
			break
		}
		proof := identity.ResolveIdentity(session, name, x.Cwd, vars, env, allowBuiltins)
		known := proof.Known()
		x.CommandIdentity = proof
		if identity.IsWrapper(base) && known {
			x.Wrapped = true
			if !registry.Is(base, registry.ShellWrapper) {
				// exec-style wrappers use PATH, not the invoking shell's builtins.
				allowBuiltins = false
			}
			expanded, ok := unwrap.Execution(base, x.Argv[1:], x.Words[1:], x.Cwd, vars, ResolveAgainstCwd)
			if !ok {
				x.Uncertain = true
				break
			}
			x.Argv, x.Words, x.Cwd = expanded.Argv, expanded.Words, expanded.Cwd
			if !session.AgentHasRuntimeSandbox {
				if normalized, err := NormalizePath(x.Cwd); err == nil {
					x.Cwd = normalized
				} else {
					x.Uncertain = true
					return x
				}
			} else if env != nil && env.Directory != nil {
				if canonical, err := env.Directory(x.Cwd); err == nil {
					x.Cwd = canonical
				} else {
					x.Uncertain = true
					if errors.Is(err, types.ErrBashTemporaryEscape) {
						x.BlockReason = err.Error()
					}
				}
			}
			if len(expanded.Argv) == 0 {
				return x
			}
			if depth == 7 {
				x.Uncertain = true
			}
			continue
		}
		x.Identity = base
		if identity.IsOpaqueCommand(base) && known {
			x.Opaque = true
			x.TrustedInterpreter = identity.IsInterpreter(base)
			x.Script = identity.InterpreterScript(base, x.Argv[1:])
			if x.Script != "" && session.AgentHasRuntimeSandbox && env != nil && env.Canonical != nil {
				resolved, err := env.Canonical(x.Script, x.Cwd)
				if err != nil {
					x.Uncertain = true
					if errors.Is(err, types.ErrBashTemporaryEscape) {
						x.BlockReason = err.Error()
					}
				} else {
					x.Script = resolved
				}
			}
			return x
		}
		if known && identity.IsOrdinaryCommand(base) {
			return x
		}
		x.Opaque = true
		if session.AgentHasRuntimeSandbox {
			if env != nil && env.Resolve != nil {
				var err error
				x.Program, err = env.Resolve(name, x.Cwd, vars)
				if errors.Is(err, types.ErrBashTemporaryEscape) {
					x.BlockReason = err.Error()
				}
			}
			if x.Program == "" && strings.ContainsAny(name, "/\\") {
				x.Program = ResolveAgainstCwd(name, x.Cwd)
			}
		} else {
			x.Program = identity.ResolvedProgram(name, x.Cwd, vars)
		}
		if x.Program == "" && strings.ContainsAny(name, "/\\") {
			x.Program = ResolveAgainstCwd(name, x.Cwd)
		}
		if x.Program != "" {
			x.Identity = x.Program
			if session.AgentHasRuntimeSandbox && env != nil && env.Inspect != nil {
				if header, _, err := env.Inspect(x.Program); err == nil && !strings.ContainsRune(header, 0) {
					x.Script = x.Program
					if family := identity.ShebangFamily(header, session, x.Cwd, vars, env); family != "" {
						x.Identity = family
					}
				}
				return x
			}
			if host, ok := identity.ExecutableHostPath(session, x.Program); ok {
				if canonical, err := pathutil.Canonicalize(host); err == nil {
					x.Identity = canonical.Key
				}
				if script, header := identity.ScriptHeader(host); script {
					x.Script = x.Program
					family := identity.ShebangFamily(header, session, x.Cwd, vars, env)
					if family != "" {
						x.Identity = family
					}
				}
			}
		}
		return x
	}
	return x
}
