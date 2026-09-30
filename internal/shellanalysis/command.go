package shellanalysis

import (
	"path"
	"strings"
)

func CommandName(name string) string {
	return strings.TrimSuffix(strings.ToLower(path.Base(strings.ReplaceAll(name, "\\", "/"))), ".exe")
}

// HookCommand removes syntactic wrappers and Git's global flags for business
// rule matching. This grants no execution trust; that still requires identity
// and content verification by the executor.
func HookCommand(argv []string) (string, []string) {
	for depth := 0; depth < 8 && len(argv) > 0; depth++ {
		base := CommandName(argv[0])
		args := argv[1:]
		switch base {
		case "env", "command", "builtin", "nohup", "nice", "timeout", "stdbuf":
			i := 0
			for i < len(args) {
				a := args[i]
				if a == "--" {
					i++
					break
				}
				if base == "env" && strings.Contains(a, "=") && !strings.HasPrefix(a, "-") {
					i++
					continue
				}
				if !strings.HasPrefix(a, "-") {
					break
				}
				i++
				if a == "-u" || a == "--unset" || a == "-C" || a == "--chdir" || a == "-n" || a == "--adjustment" || a == "-s" || a == "--signal" || a == "-k" || a == "--kill-after" || a == "-i" && base == "stdbuf" || a == "-o" || a == "-e" {
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
				if a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--exec-path" || a == "--config-env" {
					i++
				}
			}
			if i > len(args) {
				i = len(args)
			}
			return base, args[i:]
		default:
			return base, args
		}
	}
	return "", nil
}
