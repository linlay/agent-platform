package unwrap

import (
	"strconv"
	"strings"

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
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		switch entry.ID {
		case registry.CmdCommand, registry.CmdBuiltin:
			if a == "-v" || a == "-V" {
				return Expansion{Cwd: cwd}, true
			}
			if a == "-p" {
				return Expansion{Cwd: cwd}, false
			}
		case registry.CmdEnv:
			if name, value, ok := strings.Cut(a, "="); ok && !strings.HasPrefix(name, "-") {
				vars[name] = value
				i++
				continue
			}
			if a == "-i" || a == "--ignore-environment" {
				vars["PATH"] = ""
				i++
				continue
			}
			if a == "-u" || a == "--unset" || a == "-C" || a == "--chdir" {
				if i+1 >= len(args) {
					return Expansion{Cwd: cwd}, false
				}
				if a == "-C" || a == "--chdir" {
					cwd = resolveAgainstCwd(args[i+1], cwd)
				} else {
					vars[args[i+1]] = ""
				}
				i += 2
				continue
			}
			if strings.HasPrefix(a, "--chdir=") {
				cwd = resolveAgainstCwd(strings.TrimPrefix(a, "--chdir="), cwd)
				i++
				continue
			}
		case registry.CmdNice:
			if a == "-n" || a == "--adjustment" {
				if i+1 >= len(args) {
					return Expansion{Cwd: cwd}, false
				}
				i += 2
				continue
			}
			if _, err := strconv.Atoi(a); strings.HasPrefix(a, "-") && err == nil {
				i++
				continue
			}
		case registry.CmdTimeout:
			if a == "-s" || a == "--signal" || a == "-k" || a == "--kill-after" {
				if i+1 >= len(args) {
					return Expansion{Cwd: cwd}, false
				}
				i += 2
				continue
			}
			if a == "--foreground" || a == "--preserve-status" {
				i++
				continue
			}
			if !strings.HasPrefix(a, "-") {
				i++
				if i < len(args) {
					return forward(args, words, i, cwd), true
				}
				return Expansion{Cwd: cwd}, false
			}
		case registry.CmdStdbuf:
			if a == "-i" || a == "-o" || a == "-e" {
				if i+1 >= len(args) {
					return Expansion{Cwd: cwd}, false
				}
				i += 2
				continue
			}
			if len(a) > 2 && (strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "-e")) {
				i++
				continue
			}
		}
		if strings.HasPrefix(a, "-") {
			return Expansion{Cwd: cwd}, false
		}
		break
	}
	if i >= len(args) {
		return Expansion{Cwd: cwd}, false
	}
	return forward(args, words, i, cwd), true
}
