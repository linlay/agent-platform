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

// InlineProgram returns the indices in args of program text a system
// interpreter takes literally from this command: python -c, node -e/-p, perl
// -e/-E and ruby -e. With no program argument, a literal heredoc or here-string
// on stdin qualifies. Any other form, such as perl -i or python -m, is not
// inline code.
func InlineProgram(base string, args []string, words []types.SourceWord, redirects []types.Redirect) ([]int, bool) {
	literal := func(i int) bool {
		return i < len(words) && words[i].Origin == types.WordOriginSyntax && words[i].Literal
	}
	var code []int
	i := 0
	switch registry.IDOf(base) {
	case registry.CmdPython, registry.CmdPython3:
		for ; i < len(args); i++ {
			a := args[i]
			if a == "-W" || a == "-X" {
				i++
				continue
			}
			if a == "-c" {
				if i+1 < len(args) && literal(i+1) {
					return []int{i + 1}, true
				}
				return nil, false
			}
			if len(a) < 2 || a[0] != '-' || strings.Trim(a[1:], "uBIOEsSqv") != "" {
				break
			}
		}
	case registry.CmdNode:
		if len(args) > 0 && (args[0] == "-e" || args[0] == "--eval" || args[0] == "-p" || args[0] == "--print") {
			if len(args) > 1 && literal(1) {
				return []int{1}, true
			}
			return nil, false
		}
	case registry.CmdPerl, registry.CmdRuby:
		codeLetters := "eE"
		if registry.IDOf(base) == registry.CmdRuby {
			codeLetters = "e" // ruby -E sets the encoding
		}
		for ; i < len(args); i++ {
			a := args[i]
			if len(a) < 2 || a[0] != '-' || a == "--" {
				break
			}
			letters := a[1:]
			if strings.Trim(letters[:len(letters)-1], "lnpaw") != "" {
				return nil, false
			}
			last := letters[len(letters)-1]
			if strings.IndexByte(codeLetters, last) >= 0 {
				if i++; i >= len(args) || !literal(i) {
					return nil, false
				}
				code = append(code, i)
			} else if strings.IndexByte("lnpaw", last) < 0 {
				return nil, false
			}
		}
		if len(code) > 0 {
			return code, true
		}
	default:
		return nil, false
	}
	// No program argument: the interpreter reads its program from stdin.
	if i < len(args) && args[i] != "-" {
		return nil, false
	}
	stdin := false
	for _, r := range redirects {
		if !strings.HasPrefix(r.Op, "<") || r.Fd > 0 {
			continue
		}
		switch {
		case r.IsHeredoc:
			stdin = r.HeredocLiteral
		case r.Op == "<<<":
			stdin = r.TargetWord.Literal
		default:
			stdin = false
		}
	}
	return []int{}, stdin
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
