package bashsec

import "strings"

// validateUnicodeWhitespace detects Unicode whitespace characters that could
// cause parsing inconsistencies between different shell implementations and
// our validators.
func validateUnicodeWhitespace(command string) (bool, string) {
	if unicodeWsRe.MatchString(command) {
		return false, "Command contains Unicode whitespace characters that could cause parsing inconsistencies"
	}
	return true, ""
}

// validateMidWordHash detects # adjacent to non-whitespace without a preceding
// space. shell-quote treats mid-word # as a comment-start but bash treats it
// as a literal character, creating a parser differential.
func validateMidWordHash(unquotedKeepQuoteChars string) (bool, string) {
	if hasMidWordHash(unquotedKeepQuoteChars) {
		return false, "Command contains mid-word # which is parsed differently by shell-quote vs bash"
	}

	joined := backslashNewlineRe.ReplaceAllStringFunc(unquotedKeepQuoteChars, func(match string) string {
		bsCount := len(match) - 1
		if bsCount%2 == 1 {
			return strings.Repeat("\\", bsCount-1)
		}
		return match
	})
	if joined != unquotedKeepQuoteChars && hasMidWordHash(joined) {
		return false, "Command contains mid-word # which is parsed differently by shell-quote vs bash"
	}

	return true, ""
}

func hasMidWordHash(content string) bool {
	for i := 0; i < len(content); i++ {
		if content[i] != '#' || i == 0 {
			continue
		}
		prev := content[i-1]
		if isASCIISpace(prev) || prev == '\n' || prev == '\r' {
			continue
		}
		if i >= 2 && content[i-2] == '$' && content[i-1] == '{' {
			continue
		}
		return true
	}
	return false
}

// validateCommentQuoteDesync detects when an unquoted # comment contains quote
// characters that would desync downstream quote trackers.
func validateCommentQuoteDesync(command string) (bool, string) {
	if !strings.Contains(command, "#") {
		return true, ""
	}

	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	for i := 0; i < len(command); i++ {
		ch := command[i]

		if escaped {
			escaped = false
			continue
		}
		if inSingleQuote {
			if ch == '\'' {
				inSingleQuote = false
			}
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if inDoubleQuote {
			if ch == '"' {
				inDoubleQuote = false
			}
			continue
		}
		if ch == '\'' {
			inSingleQuote = true
			continue
		}
		if ch == '"' {
			inDoubleQuote = true
			continue
		}

		if ch == '#' {
			lineEnd := strings.Index(command[i+1:], "\n")
			var commentText string
			if lineEnd == -1 {
				commentText = command[i+1:]
			} else {
				commentText = command[i+1 : i+1+lineEnd]
			}
			if strings.ContainsAny(commentText, "'\"") {
				return false, "Command contains quote characters inside a # comment which can desync quote tracking"
			}
			if lineEnd == -1 {
				break
			}
			i += lineEnd + 1
		}
	}

	return true, ""
}

// validateQuotedNewline detects newlines inside quoted strings where the next
// line starts with # after trimming.
func validateQuotedNewline(command string) (bool, string) {
	if !strings.Contains(command, "\n") || !strings.Contains(command, "#") {
		return true, ""
	}

	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	for i := 0; i < len(command); i++ {
		ch := command[i]

		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && !inSingleQuote {
			escaped = true
			continue
		}
		if ch == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			continue
		}
		if ch == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			continue
		}

		if ch == '\n' && (inSingleQuote || inDoubleQuote) {
			lineStart := i + 1
			nextNewline := strings.Index(command[lineStart:], "\n")
			var nextLine string
			if nextNewline == -1 {
				nextLine = command[lineStart:]
			} else {
				nextLine = command[lineStart : lineStart+nextNewline]
			}
			if strings.HasPrefix(strings.TrimSpace(nextLine), "#") {
				return false, "Command contains a quoted newline followed by a #-prefixed line, which can hide arguments from line-based permission checks"
			}
		}
	}

	return true, ""
}
