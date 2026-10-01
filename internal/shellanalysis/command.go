package shellanalysis

import (
	"path"
	"strings"
)

func CommandName(name string) string {
	return strings.TrimSuffix(strings.ToLower(path.Base(strings.ReplaceAll(name, "\\", "/"))), ".exe")
}

// IsShellInterpreter reports POSIX-style shells whose -c operand is Bash-like
// source that can be analyzed recursively.
func IsShellInterpreter(base string) bool {
	switch base {
	case "bash", "sh", "zsh", "dash", "ksh":
		return true
	}
	return false
}

// ShellScript returns the literal script of `bash -c script [$0 args...]`.
// Login/interactive shells and unrecognized options are not analyzable: they
// load profiles or change parsing before the script runs.
func ShellScript(argv []string) (string, bool) {
	if len(argv) < 3 || !IsShellInterpreter(CommandName(argv[0])) {
		return "", false
	}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--noprofile" || a == "--norc":
			continue
		case a == "-o" || a == "+o":
			i++
			continue
		case a == "-c":
			if i+1 < len(argv) {
				return argv[i+1], true
			}
			return "", false
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			flags := a[1:]
			if strings.Trim(flags, "euxvfBEHTP") == "" {
				continue
			}
			if a[0] == '-' && strings.HasSuffix(flags, "c") && strings.Trim(strings.TrimSuffix(flags, "c"), "euxvfBEHTP") == "" {
				if i+1 < len(argv) {
					return argv[i+1], true
				}
			}
			return "", false
		default:
			return "", false
		}
	}
	return "", false
}

// UnwrapEnv returns the command executed by env(1), including the -S/--split-string
// form. ok is false when the arguments cannot be interpreted statically.
func UnwrapEnv(args []string) (vars map[string]string, command []string, chdir string, ignore bool, ok bool) {
	vars = map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return vars, args[i+1:], chdir, ignore, true
		case a == "-i" || a == "--ignore-environment" || a == "-":
			ignore = true
		case a == "-u" || a == "--unset":
			if i+1 >= len(args) {
				return nil, nil, "", false, false
			}
			i++
		case strings.HasPrefix(a, "--unset=") || strings.HasPrefix(a, "-u") && len(a) > 2:
		case a == "-C" || a == "--chdir":
			if i+1 >= len(args) {
				return nil, nil, "", false, false
			}
			i++
			chdir = args[i]
		case strings.HasPrefix(a, "--chdir="):
			chdir = strings.TrimPrefix(a, "--chdir=")
		case a == "-S" || a == "--split-string" || strings.HasPrefix(a, "-S") || strings.HasPrefix(a, "--split-string="):
			value := ""
			switch {
			case a == "-S" || a == "--split-string":
				if i+1 >= len(args) {
					return nil, nil, "", false, false
				}
				i++
				value = args[i]
			case strings.HasPrefix(a, "--split-string="):
				value = strings.TrimPrefix(a, "--split-string=")
			default:
				value = strings.TrimPrefix(a, "-S")
			}
			// env -S has its own escape and ${VAR} rules; only plain words are static.
			if strings.ContainsAny(value, "\\'\"$#") {
				return nil, nil, "", false, false
			}
			split := strings.Fields(value)
			return vars, append(split, args[i+1:]...), chdir, ignore, true
		case strings.HasPrefix(a, "-"):
			return nil, nil, "", false, false
		case strings.Contains(a, "=") && !strings.HasPrefix(a, "="):
			name, value, _ := strings.Cut(a, "=")
			vars[name] = value
		default:
			return vars, args[i:], chdir, ignore, true
		}
	}
	return vars, nil, chdir, ignore, true
}

// HookCommand removes syntactic wrappers and Git's global flags for business
// rule matching. This grants no execution trust; that still requires identity
// and content verification by the executor.
func HookCommand(argv []string) (string, []string) {
	for depth := 0; depth < 8 && len(argv) > 0; depth++ {
		base := CommandName(argv[0])
		args := argv[1:]
		switch base {
		case "env":
			_, command, _, _, ok := UnwrapEnv(args)
			if !ok || len(command) == 0 {
				return base, args
			}
			argv = command
		case "command", "builtin", "nohup", "nice", "timeout", "stdbuf":
			i := 0
			for i < len(args) {
				a := args[i]
				if a == "--" {
					i++
					break
				}
				if !strings.HasPrefix(a, "-") {
					break
				}
				i++
				if a == "-n" || a == "--adjustment" || a == "-s" || a == "--signal" || a == "-k" || a == "--kill-after" || a == "-i" && base == "stdbuf" || a == "-o" || a == "-e" {
					i++
				}
			}
			if base == "timeout" {
				i++
			}
			if i >= len(args) {
				return base, args
			}
			argv = args[i:]
		case "git":
			return base, gitSubcommandArgs(args)
		default:
			return base, args
		}
	}
	return "", nil
}

func gitSubcommandArgs(args []string) []string {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") {
			break
		}
		i++
		if a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--exec-path" || a == "--config-env" || a == "--namespace" {
			i++
		}
	}
	if i > len(args) {
		i = len(args)
	}
	return args[i:]
}
