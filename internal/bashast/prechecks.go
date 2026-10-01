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
	controlCharacterReason   = "Command contains non-printable control characters that could bypass security checks"
	unicodeWhitespaceReason  = "Command contains Unicode whitespace characters that could cause parsing inconsistencies"
	zshTildeBracketReason    = "Command contains Zsh-style parameter expansion"
	zshEqualsExpansionReason = "Command contains Zsh equals expansion"
)

func runPrechecks(command string) (bool, string) {
	switch {
	case controlCharRe.MatchString(command):
		return false, controlCharacterReason
	case unicodeWhitespaceRe.MatchString(unquotedText(command)):
		return false, unicodeWhitespaceReason
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

func unquotedText(command string) string {
	var b strings.Builder
	var quote rune
	escaped := false
	for _, r := range command {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
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
	switch reason {
	case controlCharacterReason,
		unicodeWhitespaceReason,
		zshTildeBracketReason,
		zshEqualsExpansionReason:
		// Backslash-escaped whitespace remains unparsed by the reviewer and is
		// approval-only; brace expansion is expanded by the walker.
		return true
	default:
		return false
	}
}
