package unwrap

import (
	"strconv"
	"strings"
)

// Execution expands an already trusted wrapper using the invocation's cwd resolver.
func Execution(base string, args []string, cwd string, vars map[string]string, resolveAgainstCwd func(string, string) string) ([]string, string, bool) {
	if base == "env" {
		assignments, command, chdir, ignore, ok := UnwrapEnv(args)
		if !ok {
			return nil, cwd, false
		}
		if len(command) == 0 {
			// Without a command env only prints the environment, like printenv.
			return nil, cwd, true
		}
		if ignore {
			vars["PATH"] = ""
		}
		for name, value := range assignments {
			vars[name] = value
		}
		if chdir != "" {
			cwd = resolveAgainstCwd(chdir, cwd)
		}
		return command, cwd, true
	}
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		switch base {
		case "command", "builtin":
			if a == "-v" || a == "-V" {
				return nil, cwd, true
			}
			if a == "-p" {
				return nil, cwd, false
			}
		case "env":
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
					return nil, cwd, false
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
		case "nice":
			if a == "-n" || a == "--adjustment" {
				if i+1 >= len(args) {
					return nil, cwd, false
				}
				i += 2
				continue
			}
			if _, err := strconv.Atoi(a); strings.HasPrefix(a, "-") && err == nil {
				i++
				continue
			}
		case "timeout":
			if a == "-s" || a == "--signal" || a == "-k" || a == "--kill-after" {
				if i+1 >= len(args) {
					return nil, cwd, false
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
					return args[i:], cwd, true
				}
				return nil, cwd, false
			}
		case "stdbuf":
			if a == "-i" || a == "-o" || a == "-e" {
				if i+1 >= len(args) {
					return nil, cwd, false
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
			return nil, cwd, false
		}
		break
	}
	if i >= len(args) {
		return nil, cwd, false
	}
	return args[i:], cwd, true
}
