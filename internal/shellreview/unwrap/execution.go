package unwrap

import (
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
)

// Execution expands an already trusted wrapper using the invocation's cwd resolver.
func Execution(base string, args []string, words []types.SourceWord, cwd string, vars map[string]string, resolveAgainstCwd func(string, string) string) (Expansion, bool) {
	entry, _ := registry.Lookup(base)
	if entry.ID == registry.CmdEnv {
		expanded, ok := Env(args)
		if !ok {
			return Expansion{Cwd: cwd}, false
		}
		if len(expanded.Command) == 0 {
			return Expansion{Cwd: cwd}, true
		}
		if expanded.Ignore {
			vars["PATH"] = ""
		}
		for name, value := range expanded.Assignments {
			vars[name] = value
		}
		if expanded.Chdir != "" {
			cwd = resolveAgainstCwd(expanded.Chdir, cwd)
		}
		return Expansion{Argv: expanded.Command, Words: mapWords(expanded.refs, words, len(args)), Cwd: cwd}, true
	}
	index, terminal, ok := wrapperIndex(entry.ID, args, false, false)
	if !ok {
		return Expansion{Cwd: cwd}, false
	}
	if terminal {
		return Expansion{Cwd: cwd}, true
	}
	return forward(args, words, index, cwd), true
}
