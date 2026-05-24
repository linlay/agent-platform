package bashsec

import (
	"fmt"
	"path/filepath"
	"strings"
)

func normalizedCommandBase(command string) string {
	return strings.ToLower(filepath.Base(strings.TrimSpace(command)))
}

func deterministicCommandChain(argv []string) [][]string {
	var chain [][]string
	current := append([]string(nil), argv...)
	for depth := 0; depth < 8 && len(current) > 0; depth++ {
		chain = append(chain, current)
		next, ok := unwrapDeterministicWrapperArgv(current)
		if !ok || len(next) == 0 {
			break
		}
		current = next
	}
	return chain
}

func unwrapDeterministicWrapperArgv(argv []string) ([]string, bool) {
	if len(argv) == 0 {
		return nil, false
	}
	switch normalizedCommandBase(argv[0]) {
	case "command", "builtin":
		return unwrapCommandBuiltin(argv[1:])
	case "env":
		return unwrapEnv(argv[1:])
	case "nohup":
		return argv[1:], len(argv) > 1
	case "nice":
		return unwrapNice(argv[1:])
	case "timeout":
		return unwrapTimeout(argv[1:])
	case "stdbuf":
		return unwrapStdbuf(argv[1:])
	default:
		return nil, false
	}
}

func unwrapCommandBuiltin(args []string) ([]string, bool) {
	idx := 0
	for idx < len(args) && strings.HasPrefix(args[idx], "-") {
		idx++
	}
	if idx >= len(args) {
		return nil, false
	}
	return args[idx:], true
}

func unwrapEnv(args []string) ([]string, bool) {
	idx := 0
	for idx < len(args) {
		arg := args[idx]
		if arg == "--" {
			idx++
			break
		}
		if isVarAssignment(arg) {
			idx++
			continue
		}
		if arg == "-u" || arg == "--unset" || arg == "-C" || arg == "--chdir" {
			idx += 2
			continue
		}
		if strings.HasPrefix(arg, "-u") && len(arg) > 2 {
			idx++
			continue
		}
		if strings.HasPrefix(arg, "--unset=") || strings.HasPrefix(arg, "--chdir=") {
			idx++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			idx++
			continue
		}
		break
	}
	if idx >= len(args) {
		return nil, false
	}
	return args[idx:], true
}

func unwrapNice(args []string) ([]string, bool) {
	idx := 0
	for idx < len(args) {
		arg := args[idx]
		if arg == "-n" || arg == "--adjustment" {
			idx += 2
			continue
		}
		if strings.HasPrefix(arg, "-n") || strings.HasPrefix(arg, "--adjustment=") {
			idx++
			continue
		}
		if strings.HasPrefix(arg, "-") && len(arg) > 1 && isNumericNiceAdjustment(arg) {
			idx++
			continue
		}
		break
	}
	if idx >= len(args) {
		return nil, false
	}
	return args[idx:], true
}

func isNumericNiceAdjustment(arg string) bool {
	trimmed := strings.TrimLeft(arg, "-+")
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func unwrapTimeout(args []string) ([]string, bool) {
	idx := 0
	for idx < len(args) {
		arg := args[idx]
		if arg == "--" {
			idx++
			break
		}
		if arg == "-s" || arg == "--signal" || arg == "-k" || arg == "--kill-after" {
			idx += 2
			continue
		}
		if strings.HasPrefix(arg, "--signal=") || strings.HasPrefix(arg, "--kill-after=") {
			idx++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			idx++
			continue
		}
		idx++
		break
	}
	if idx >= len(args) {
		return nil, false
	}
	return args[idx:], true
}

func unwrapStdbuf(args []string) ([]string, bool) {
	idx := 0
	for idx < len(args) {
		arg := args[idx]
		if arg == "--" {
			idx++
			break
		}
		if arg == "-i" || arg == "-o" || arg == "-e" {
			idx += 2
			continue
		}
		if strings.HasPrefix(arg, "-i") || strings.HasPrefix(arg, "-o") || strings.HasPrefix(arg, "-e") {
			idx++
			continue
		}
		break
	}
	if idx >= len(args) {
		return nil, false
	}
	return args[idx:], true
}

func reviewRuntimeWrapperCommand(command string, argv []string) ReviewResult {
	if len(argv) == 0 {
		return ReviewResult{Decision: ReviewAllow}
	}
	switch normalizedCommandBase(argv[0]) {
	case "xargs":
		if inner, ok := xargsInnerArgv(argv); ok && len(inner) > 0 && isDangerousASTCommand(normalizedCommandBase(inner[0])) {
			return blockReview(fmt.Sprintf("Command uses unsupported shell builtin: %s", normalizedCommandBase(inner[0])))
		}
		return approvalReview(command, "Command uses xargs with runtime-provided arguments", RuleKeyRuntimeWrapperXargs, LevelRuntimeWrapper)
	case "find":
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
