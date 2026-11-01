package unwrap

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
