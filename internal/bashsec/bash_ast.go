package bashsec

import (
	"fmt"
	"strings"

	"agent-platform/internal/bashast"
)

func reviewFromAST(command string, result bashast.ParseResult, embeddedScripts []bashast.EmbeddedScript) ReviewResult {
	legacy := reviewLegacyCompatibleWithAST(command, result)
	if legacy.Decision != ReviewAllow {
		return legacy
	}

	for _, cmd := range result.Commands {
		if review := reviewASTCommand(command, cmd); review.Decision != ReviewAllow {
			return review
		}
	}
	for _, script := range embeddedScripts {
		if bashast.IsDangerousEmbeddedScript(script) {
			return blockReview(fmt.Sprintf("Command contains dangerous embedded %s code", script.Language))
		}
	}
	return ReviewResult{Decision: ReviewAllow}
}

func reviewASTCommand(command string, cmd bashast.SimpleCommand) ReviewResult {
	if len(cmd.Argv) == 0 {
		return ReviewResult{Decision: ReviewAllow}
	}
	commandChain := deterministicCommandChain(cmd.Argv)
	for _, argv := range commandChain {
		if len(argv) == 0 {
			continue
		}
		base := normalizedCommandBase(argv[0])
		if isDangerousASTCommand(base) {
			return blockReview(fmt.Sprintf("Command uses unsupported shell builtin: %s", base))
		}
		if review := reviewRuntimeWrapperCommand(command, argv); review.Decision != ReviewAllow {
			return review
		}
	}
	if bashast.HasDangerousJQFileFlag(cmd) {
		return blockReview("Command uses jq file loading which could read sensitive files")
	}
	for _, arg := range cmd.Argv {
		if strings.Contains(arg, "/proc/") && strings.Contains(arg, "/environ") {
			return blockReview("Command accesses /proc/*/environ which could expose sensitive environment variables")
		}
	}
	for _, redir := range cmd.Redirects {
		if review := reviewASTRedirect(command, redir); review.Decision != ReviewAllow {
			return review
		}
	}
	return ReviewResult{Decision: ReviewAllow}
}

func reviewASTRedirect(command string, redir bashast.Redirect) ReviewResult {
	if redir.IsHeredoc {
		return ReviewResult{Decision: ReviewAllow}
	}
	op := strings.TrimSpace(redir.Op)
	target := strings.TrimSpace(redir.Target)
	if containsASTPlaceholder(target) {
		return approvalReview(command, "Command contains redirection target that cannot be resolved statically", RuleKeyRedirections, LevelRedirections)
	}
	if strings.Contains(target, "/proc/") && strings.Contains(target, "/environ") {
		return blockReview("Command accesses /proc/*/environ which could expose sensitive environment variables")
	}
	if isSafeASTRedirect(redir) {
		return ReviewResult{Decision: ReviewAllow}
	}
	switch op {
	case "<", "<&", "<>", "<<<":
		return blockReview("Command contains input redirection (<) which could read sensitive files")
	case ">", ">>", ">|", ">&", "&>", "&>>":
		return approvalReview(command, outputRedirectionReason, RuleKeyRedirections, LevelRedirections)
	default:
		if strings.Contains(op, "<") {
			return blockReview("Command contains input redirection (<) which could read sensitive files")
		}
		if strings.Contains(op, ">") {
			return approvalReview(command, outputRedirectionReason, RuleKeyRedirections, LevelRedirections)
		}
	}
	return ReviewResult{Decision: ReviewAllow}
}

func containsASTPlaceholder(value string) bool {
	return strings.Contains(value, bashast.CommandSubstitutionPlaceholder) ||
		strings.Contains(value, bashast.TrackedVariablePlaceholder)
}

func isSafeASTRedirect(redir bashast.Redirect) bool {
	target := strings.TrimSpace(redir.Target)
	switch strings.TrimSpace(redir.Op) {
	case ">&":
		return redir.Fd == 2 && target == "1"
	case ">":
		return target == "/dev/null"
	case "<":
		return target == "/dev/null"
	case "&>":
		return target == "/dev/null"
	default:
		return false
	}
}

func isDangerousASTCommand(base string) bool {
	if astDangerousCommands[base] {
		return true
	}
	return zshDangerousCommands[base]
}
