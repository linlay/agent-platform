package bashast

import (
	"regexp"
	"strings"
)

var (
	controlCharRe          = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")
	unicodeWhitespaceRe    = regexp.MustCompile("[\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000\uFEFF]")
	zshTildeBracketRe      = regexp.MustCompile(`~\[`)
	zshEqualsExpansionRe   = regexp.MustCompile(`(?:^|[\s;&|])=[A-Za-z_]`)
	backslashWhitespaceMsg = "Command contains backslash-escaped whitespace that could alter command parsing"
)

const (
	controlCharacterReason       = "Command contains non-printable control characters that could bypass security checks"
	unicodeWhitespaceReason      = "Command contains Unicode whitespace characters that could cause parsing inconsistencies"
	zshTildeBracketReason        = "Command contains Zsh-style parameter expansion"
	zshEqualsExpansionReason     = "Command contains Zsh equals expansion"
	braceExpansionPrecheckReason = "Command contains brace expansion that could alter command parsing"
)

func runPrechecks(command string) (bool, string) {
	switch {
	case controlCharRe.MatchString(command):
		return false, controlCharacterReason
	case unicodeWhitespaceRe.MatchString(command):
		return false, unicodeWhitespaceReason
	case hasBackslashEscapedWhitespace(command):
		return false, backslashWhitespaceMsg
	case zshTildeBracketRe.MatchString(command):
		return false, zshTildeBracketReason
	case zshEqualsExpansionRe.MatchString(command):
		return false, zshEqualsExpansionReason
	case hasUnquotedBraceExpansion(command):
		return false, braceExpansionPrecheckReason
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

func hasUnquotedBraceExpansion(command string) bool {
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false
	runes := []rune(command)
	for idx := 0; idx < len(runes); idx++ {
		r := runes[idx]
		switch {
		case escaped:
			escaped = false
		case r == '\\' && !inSingleQuote:
			escaped = true
		case r == '\'' && !inDoubleQuote:
			inSingleQuote = !inSingleQuote
		case r == '"' && !inSingleQuote:
			inDoubleQuote = !inDoubleQuote
		case r == '{' && !inSingleQuote && !inDoubleQuote:
			if braceContainsExpansionOperator(runes[idx+1:]) {
				return true
			}
		}
	}
	return false
}

func braceContainsExpansionOperator(rest []rune) bool {
	for idx := 0; idx < len(rest); idx++ {
		switch rest[idx] {
		case '}':
			return false
		case ',':
			return true
		case '.':
			if idx+1 < len(rest) && rest[idx+1] == '.' {
				return true
			}
		case '{':
			return false
		}
	}
	return false
}

func IsHardBlockReason(reason string) bool {
	reason = strings.TrimSpace(reason)
	switch reason {
	case controlCharacterReason,
		unicodeWhitespaceReason,
		backslashWhitespaceMsg,
		zshTildeBracketReason,
		zshEqualsExpansionReason,
		braceExpansionPrecheckReason:
		return true
	default:
		return false
	}
}
