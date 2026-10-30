package bashast

import (
	"regexp"
	"strings"

	"agent-platform/internal/shellreview/syntax"
)

var (
	zshTildeBracketRe      = regexp.MustCompile(`~\[`)
	zshEqualsExpansionRe   = regexp.MustCompile(`(?:^|[\s;&|])=[A-Za-z_]`)
	backslashWhitespaceMsg = "Command contains backslash-escaped whitespace that could alter command parsing"
)

const (
	zshTildeBracketReason    = "Command contains Zsh-style parameter expansion"
	zshEqualsExpansionReason = "Command contains Zsh equals expansion"
)

func runPrechecks(command string) (bool, string) {
	if reason := syntax.CheckText(command); reason != "" {
		return false, reason
	}
	switch {
	case hasBackslashEscapedWhitespace(command):
		return false, backslashWhitespaceMsg
	case zshTildeBracketRe.MatchString(command):
		return false, zshTildeBracketReason
	case zshEqualsExpansionRe.MatchString(command):
		return false, zshEqualsExpansionReason
	default:
		return true, ""
	}
}

func hasBackslashEscapedWhitespace(command string) bool {
	inSingleQuote := false
	inDoubleQuote := false
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && !inSingleQuote {
			if !inDoubleQuote && i+1 < len(runes) && (runes[i+1] == ' ' || runes[i+1] == '\t') {
				return true
			}
			i++
			continue
		}
		if r == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			continue
		}
		if r == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			continue
		}
	}
	return false
}

func IsHardBlockReason(reason string) bool {
	reason = strings.TrimSpace(reason)
	if syntax.IsTextRejection(reason) {
		return true
	}
	switch reason {
	case zshTildeBracketReason,
		zshEqualsExpansionReason:
		// Backslash-escaped whitespace remains unparsed by the reviewer and is
		// approval-only; brace expansion is expanded by the walker.
		return true
	default:
		return false
	}
}
