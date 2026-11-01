package identity

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	. "agent-platform/internal/contracts"
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
	if base == "env" {
		if !RecognizedProgram(session, words[0], cwd, vars, environment, false) {
			return ""
		}
		// env -S quoting is deliberately not guessed from a shebang string.
		localVars := CloneStringMap(vars)
		if localVars == nil {
			localVars = map[string]string{}
		}
		args, dir, ok := unwrap.Execution("env", words[1:], cwd, localVars)
		if !ok || len(args) == 0 {
			return ""
		}
		words, cwd, vars = args, dir, localVars
		base = CommandFamily(words[0])
	}
	if IsInterpreter(base) && RecognizedProgram(session, words[0], cwd, vars, environment, false) {
		return base
	}
	return ""
}

func IsInterpreter(base string) bool {
	switch base {
	case "bash", "sh", "zsh", "dash", "ksh", "python", "python3", "node", "ruby", "perl", "powershell", "pwsh", "cmd":
		return true
	}
	return false
}

func InterpreterScript(base string, args []string) string {
	if !IsInterpreter(base) {
		return ""
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		lower := strings.ToLower(a)
		if base == "powershell" || base == "pwsh" {
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
		if base == "cmd" {
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
		switch base {
		case "bash", "sh", "zsh", "dash", "ksh":
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
		case "python", "python3":
			if a == "-W" || a == "-X" {
				i++
				continue
			}
			if strings.Trim(a[1:], "uBIOEsSqv") != "" {
				return ""
			}
		case "node":
			return ""
		case "ruby":
			if a != "-w" {
				return ""
			}
		case "perl":
			if a != "-w" {
				return ""
			}
		default:
			return ""
		}
	}
	return ""
}
