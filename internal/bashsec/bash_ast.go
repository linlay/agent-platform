package bashsec

import (
	"fmt"
	"strings"

	"agent-platform/internal/bashast"
	"agent-platform/internal/shellanalysis"
)

func reviewFromAST(original, command string, result bashast.ParseResult, variables map[string]string, depth int) ReviewResult {
	review := ReviewResult{Decision: ReviewAllow}
	if blocked := reviewText(command, result); blocked.Decision == ReviewBlock {
		return blocked
	}
	for _, cmd := range result.Commands {
		next := reviewASTCommand(original, cmd)
		if next.Decision == ReviewBlock {
			return next
		}
		if next.Decision == ReviewRequiresApproval && review.Decision == ReviewAllow {
			review = next
		}
		if script, ok := shellanalysis.ShellScript(cmd.Argv); ok && depth < maxScriptDepth {
			inner := reviewScript(original, script, variables, depth+1)
			if inner.Decision == ReviewBlock {
				return inner
			}
			if inner.Decision == ReviewRequiresApproval && review.Decision == ReviewAllow {
				review = inner
			}
		}
	}
	return review
}

func reviewASTCommand(command string, cmd bashast.SimpleCommand) ReviewResult {
	result := ReviewResult{Decision: ReviewAllow}
	for _, argv := range deterministicCommandChain(cmd.Argv) {
		if len(argv) == 0 {
			continue
		}
		base := normalizedCommandBase(argv[0])
		if isDangerousASTCommand(base) {
			return blockReview(fmt.Sprintf("Command uses unsupported shell builtin: %s", base))
		}
		if base == "fc" && hasFlagLetter(argv[1:], 'e') {
			return blockReview("Command uses 'fc -e' which can execute arbitrary commands via editor")
		}
		if review := reviewRuntimeWrapperCommand(command, argv); review.Decision == ReviewBlock {
			return review
		} else if review.Decision == ReviewRequiresApproval {
			result = review
		}
	}
	for _, arg := range cmd.Argv {
		if isProcEnviron(arg) {
			return blockReview(procEnvironReason)
		}
	}
	for _, redir := range cmd.Redirects {
		if !redir.IsHeredoc && isProcEnviron(redir.Target) {
			return blockReview(procEnvironReason)
		}
	}
	return result
}

func hasFlagLetter(args []string, letter byte) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.IndexByte(arg[1:], letter) >= 0 {
			return true
		}
	}
	return false
}

func isDangerousASTCommand(base string) bool {
	return astDangerousCommands[base] || zshDangerousCommands[base]
}
