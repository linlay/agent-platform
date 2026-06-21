package bashsec

import "strings"

// quoteExtraction holds quoted-content-stripping variants used by legacy
// validators.
type quoteExtraction struct {
	// withDoubleQuotes: single-quoted content removed, double-quoted content preserved.
	withDoubleQuotes string
	// fullyUnquoted: both single and double quoted content removed.
	fullyUnquoted string
	// unquotedKeepQuoteChars: quoted content removed but quote delimiters preserved.
	// Used by mid-word hash detection.
	unquotedKeepQuoteChars string
}

// extractQuotedContent parses a command string and produces variants with
// quoted content stripped. This tracks single-quote and double-quote state
// and handles backslash escaping (which is not active inside single quotes).
func extractQuotedContent(command string) quoteExtraction {
	var withDQ, fullyUQ, keepQC strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	for i := 0; i < len(command); i++ {
		ch := command[i]

		if escaped {
			escaped = false
			if !inSingleQuote {
				withDQ.WriteByte(ch)
			}
			if !inSingleQuote && !inDoubleQuote {
				fullyUQ.WriteByte(ch)
				keepQC.WriteByte(ch)
			}
			continue
		}

		if ch == '\\' && !inSingleQuote {
			escaped = true
			if !inSingleQuote {
				withDQ.WriteByte(ch)
			}
			if !inSingleQuote && !inDoubleQuote {
				fullyUQ.WriteByte(ch)
				keepQC.WriteByte(ch)
			}
			continue
		}

		if ch == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			keepQC.WriteByte(ch)
			continue
		}

		if ch == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			keepQC.WriteByte(ch)
			continue
		}

		if !inSingleQuote {
			withDQ.WriteByte(ch)
		}
		if !inSingleQuote && !inDoubleQuote {
			fullyUQ.WriteByte(ch)
			keepQC.WriteByte(ch)
		}
	}

	return quoteExtraction{
		withDoubleQuotes:       withDQ.String(),
		fullyUnquoted:          fullyUQ.String(),
		unquotedKeepQuoteChars: keepQC.String(),
	}
}

// stripSafeRedirections removes known-safe redirect patterns from unquoted content:
//   - 2>&1
//   - [012]?>/dev/null
//   - </dev/null
//
// All patterns require a trailing boundary (whitespace or end-of-string) to
// prevent partial matches like >/dev/nullo.
func stripSafeRedirections(content string) string {
	content = safeRedir2To1.ReplaceAllString(content, "")
	content = safeRedirDevNull.ReplaceAllString(content, "")
	content = safeRedirInputDevNull.ReplaceAllString(content, "")
	return content
}

// hasUnescapedChar checks whether content contains an unescaped occurrence of
// the given single byte. Backslash-escaped characters are skipped.
func hasUnescapedChar(content string, ch byte) bool {
	i := 0
	for i < len(content) {
		if content[i] == '\\' && i+1 < len(content) {
			i += 2
			continue
		}
		if content[i] == ch {
			return true
		}
		i++
	}
	return false
}

// isEscapedAtPosition checks whether the character at pos is escaped by
// counting consecutive backslashes before it. Odd count means escaped.
func isEscapedAtPosition(content string, pos int) bool {
	count := 0
	i := pos - 1
	for i >= 0 && content[i] == '\\' {
		count++
		i--
	}
	return count%2 == 1
}
