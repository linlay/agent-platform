package resources

import (
	"slices"
	"strings"

	"agent-platform/internal/shellreview/identity"
	"agent-platform/internal/shellreview/types"
)

// OpaquePathArguments returns arguments that visibly name local paths: "." and
// "..", or words with a separator or ~. Bare words are not guessed from the
// filesystem; a module name is not a path merely because such a file exists.
// Programs with modeled grammars (jq, tar, ...) never reach this heuristic.
func OpaquePathArguments(x types.BashExecution) []string {
	var paths []string
	rawScript := ""
	if x.TrustedInterpreter && len(x.Argv) > 1 {
		rawScript = identity.InterpreterScript(identity.CommandFamily(x.Argv[0]), x.Argv[1:])
	}
	skipNext := false
	for i, arg := range x.Argv[1:] {
		if skipNext || slices.Contains(x.InlineArgs, i+1) {
			skipNext = false
			continue
		}
		if arg == "-c" || arg == "-e" || arg == "--eval" || arg == "-m" {
			skipNext = true
			continue
		}
		if arg == x.Script || arg == rawScript || arg == "-" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			_, value, ok := strings.Cut(arg, "=")
			if !ok {
				continue
			}
			arg = value
		}
		if strings.Contains(arg, "://") || arg == "" {
			continue
		}
		if arg == "." || arg == ".." || strings.ContainsAny(arg, "/\\") || strings.HasPrefix(arg, "~") {
			paths = append(paths, arg)
		}
	}
	return paths
}
