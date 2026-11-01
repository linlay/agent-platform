package shellreview

import (
	"fmt"
	"strings"

	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/unwrap"
)

// normalizedCommandBase shares command-name normalization with the access
// policy and hook matching so a ".exe" suffix means the same thing everywhere.
func normalizedCommandBase(command string) string {
	return unwrap.CommandName(command)
}

func deterministicCommandChain(argv []string) [][]string {
	var chain [][]string
	type candidate struct {
		argv  []string
		depth int
	}
	queue := []candidate{{argv: append([]string(nil), argv...)}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= 8 || len(current.argv) == 0 {
			continue
		}
		chain = append(chain, current.argv)
		for _, next := range unwrap.InspectionWrappers(current.argv) {
			if len(next) > 0 {
				queue = append(queue, candidate{argv: next, depth: current.depth + 1})
			}
		}
	}
	return chain
}

func reviewRuntimeWrapperCommand(command string, argv []string) ReviewResult {
	if len(argv) == 0 {
		return ReviewResult{Decision: ReviewAllow}
	}
	switch registry.IDOf(normalizedCommandBase(argv[0])) {
	case registry.CmdXargs:
		if inner, ok := xargsInnerArgv(argv); ok && len(inner) > 0 && isDangerousASTCommand(normalizedCommandBase(inner[0])) {
			return blockReview(fmt.Sprintf("Command uses unsupported shell builtin: %s", normalizedCommandBase(inner[0])))
		}
		return approvalReview(command, "Command uses xargs with runtime-provided arguments", RuleKeyRuntimeWrapperXargs, LevelRuntimeWrapper)
	case registry.CmdFind:
		foundExec := false
		for idx := 1; idx < len(argv); idx++ {
			if argv[idx] != "-exec" && argv[idx] != "-execdir" {
				continue
			}
			foundExec = true
			if idx+1 < len(argv) && isDangerousASTCommand(normalizedCommandBase(argv[idx+1])) {
				return blockReview(fmt.Sprintf("Command uses unsupported shell builtin: %s", normalizedCommandBase(argv[idx+1])))
			}
		}
		if foundExec {
			return approvalReview(command, "Command uses find -exec with runtime-selected paths", RuleKeyRuntimeWrapperFindExec, LevelRuntimeWrapper)
		}
	}
	return ReviewResult{Decision: ReviewAllow}
}

func xargsInnerArgv(argv []string) ([]string, bool) {
	idx := 1
	for idx < len(argv) {
		arg := argv[idx]
		if arg == "--" {
			idx++
			break
		}
		if arg == "-I" || arg == "-E" || arg == "-d" || arg == "-n" || arg == "-P" || arg == "-s" {
			idx += 2
			continue
		}
		if strings.HasPrefix(arg, "-I") || strings.HasPrefix(arg, "-E") || strings.HasPrefix(arg, "-d") ||
			strings.HasPrefix(arg, "-n") || strings.HasPrefix(arg, "-P") || strings.HasPrefix(arg, "-s") {
			idx++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			idx++
			continue
		}
		break
	}
	if idx >= len(argv) {
		return nil, false
	}
	return argv[idx:], true
}
