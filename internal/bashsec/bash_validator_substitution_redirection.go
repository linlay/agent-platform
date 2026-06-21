package bashsec

import (
	"fmt"
	"strings"
)

// validateDangerousPatterns checks for command substitution patterns and
// unescaped backticks in the unquoted content.
func validateDangerousPatterns(unquotedContent string) (bool, string) {
	if hasUnescapedChar(unquotedContent, '`') {
		return false, "Command contains backticks (`) for command substitution"
	}

	for _, p := range commandSubstitutionPatterns {
		if p.re.MatchString(unquotedContent) {
			return false, fmt.Sprintf("Command contains %s", p.msg)
		}
	}

	return true, ""
}

// validateRedirections detects input (<) and output (>) redirection in
// unquoted content after safe redirections like 2>&1 and >/dev/null have
// been stripped.
func validateRedirections(fullyUnquotedContent string) (bool, string) {
	if hasInputRedirection(fullyUnquotedContent) {
		return false, "Command contains input redirection (<) which could read sensitive files"
	}
	if hasOutputRedirection(fullyUnquotedContent) {
		return false, outputRedirectionReason
	}
	return true, ""
}

func hasInputRedirection(content string) bool {
	return strings.Contains(content, "<")
}

func hasOutputRedirection(content string) bool {
	return strings.Contains(content, ">")
}

func isPythonInlineCommandWithTripleQuotes(command, baseCommand string) bool {
	base := strings.TrimSpace(baseCommand)
	if base != "python" && base != "python3" {
		return false
	}
	if !pythonInlineCommandRe.MatchString(command) {
		return false
	}
	return strings.Contains(command, "'''") || strings.Contains(command, `"""`)
}
