package identity

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	. "agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
	"agent-platform/internal/shellreview/unwrap"
)

func ScriptHeader(path string) (bool, string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false, ""
	}
	f, err := os.Open(path)
	if err != nil {
		return false, ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil || strings.ContainsRune(string(data), 0) {
		return false, ""
	}
	return true, string(data)
}

func ShebangFamily(header string, session QuerySession, cwd string, vars map[string]string, environment *types.BashEnvironment) string {
	line := strings.SplitN(header, "\n", 2)[0]
	if !strings.HasPrefix(line, "#!") {
		return ""
	}
	words := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(words) == 0 {
		return ""
	}
	base := CommandFamily(words[0])
	if registry.IDOf(base) == registry.CmdEnv {
		if !ResolveIdentity(session, words[0], cwd, vars, environment, false).Known() {
			return ""
		}
		// env -S quoting is deliberately not guessed from a shebang string.
		localVars := CloneStringMap(vars)
		if localVars == nil {
			localVars = map[string]string{}
		}
		expanded, ok := unwrap.Execution(registry.Name(registry.CmdEnv), words[1:], nil, cwd, localVars, ResolveAgainstCwd)
		if !ok || len(expanded.Argv) == 0 {
			return ""
		}
		words, cwd, vars = expanded.Argv, expanded.Cwd, localVars
		base = CommandFamily(words[0])
	}
	if IsInterpreter(base) && ResolveIdentity(session, words[0], cwd, vars, environment, false).Known() {
		return base
	}
	return ""
}

func IsInterpreter(base string) bool {
	return base == strings.ToLower(base) && registry.Is(base, registry.Interpreter)
}

func InterpreterScript(base string, args []string) string {
	if !IsInterpreter(base) {
		return ""
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		lower := strings.ToLower(a)
		id := registry.IDOf(base)
		if id == registry.CmdPowershell || id == registry.CmdPwsh {
			switch lower {
			case "-file", "-f":
				if i+1 < len(args) {
					return args[i+1]
				}
				return ""
			case "-noprofile", "-noninteractive", "-nologo":
				continue
			case "-executionpolicy":
				i++
				continue
			default:
				return ""
			}
		}
		if id == registry.CmdCmd {
			if lower == "/c" && i+1 < len(args) {
				p := args[i+1]
				ext := strings.ToLower(filepath.Ext(p))
				if ext == ".bat" || ext == ".cmd" {
					return p
				}
			}
			return ""
		}
		if a == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if !strings.HasPrefix(a, "-") {
			return a
		}
		if a == "-" {
			return ""
		}
		switch id {
		case registry.CmdBash, registry.CmdSh, registry.CmdZsh, registry.CmdDash, registry.CmdKsh:
			if a == "--noprofile" || a == "--norc" {
				continue
			}
			if a == "-o" || a == "-O" {
				i++
				continue
			}
			if strings.Trim(a[1:], "euxvnrfBEmPT") != "" {
				return ""
			}
		case registry.CmdPython, registry.CmdPython3:
			if a == "-W" || a == "-X" {
				i++
				continue
			}
			if strings.Trim(a[1:], "uBIOEsSqv") != "" {
				return ""
			}
		case registry.CmdNode:
			return ""
		case registry.CmdRuby:
			if a != "-w" {
				return ""
			}
		case registry.CmdPerl:
			if a != "-w" {
				return ""
			}
		default:
			return ""
		}
	}
	return ""
}
