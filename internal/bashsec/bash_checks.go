package bashsec

import (
	"regexp"
	"strings"

	"agent-platform/internal/bashast"
)

const procEnvironReason = "Command accesses /proc/*/environ which could expose sensitive environment variables"

var (
	ifsRe         = regexp.MustCompile(`\$IFS|\$\{[^}]*IFS`)
	procEnvironRe = regexp.MustCompile(`/proc/.*/environ`)
)

func isProcEnviron(value string) bool {
	return strings.Contains(value, "/proc/") && strings.Contains(value, "/environ")
}

// reviewText keeps the few source-level checks that the AST cannot express:
// IFS changes word splitting for the executing shell and /proc/*/environ
// exposes other processes' secrets. Heredoc bodies are data and are skipped.
func reviewText(command string, result bashast.ParseResult) ReviewResult {
	text := maskHeredocBodies(command, result)
	if ifsRe.MatchString(text) {
		return blockReview("Command contains IFS variable usage which could bypass security validation")
	}
	if procEnvironRe.MatchString(text) {
		return blockReview(procEnvironReason)
	}
	return ReviewResult{Decision: ReviewAllow}
}

func maskHeredocBodies(command string, result bashast.ParseResult) string {
	var masked []byte
	for _, cmd := range result.Commands {
		for _, redirect := range cmd.Redirects {
			start, end, ok := heredocMaskRange(command, redirect)
			if !ok {
				continue
			}
			if masked == nil {
				masked = []byte(command)
			}
			for idx := start; idx < end; idx++ {
				masked[idx] = ' '
			}
		}
	}
	if masked == nil {
		return command
	}
	return string(masked)
}

func heredocMaskRange(command string, redirect bashast.Redirect) (int, int, bool) {
	if !redirect.IsHeredoc {
		return 0, 0, false
	}
	start, end := redirect.HeredocBodyStart, redirect.HeredocBodyEnd
	if start < 0 || end < start || start > len(command) {
		return 0, 0, false
	}
	if end > len(command) {
		end = len(command)
	}
	return start, end, true
}
