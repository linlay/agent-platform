package bashsec

// validateBackslashEscapedWhitespace detects backslash before space or tab
// outside of quotes. In bash, `echo\ test` is one token but parsers may
// interpret it differently, enabling path traversal attacks.
func validateBackslashEscapedWhitespace(command string) (bool, string) {
	if hasBackslashEscapedWS(command) {
		return false, "Command contains backslash-escaped whitespace that could alter command parsing"
	}
	return true, ""
}

func hasBackslashEscapedWS(command string) bool {
	inSingleQuote := false
	inDoubleQuote := false

	for i := 0; i < len(command); i++ {
		ch := command[i]

		if ch == '\\' && !inSingleQuote {
			if !inDoubleQuote && i+1 < len(command) {
				next := command[i+1]
				if next == ' ' || next == '\t' {
					return true
				}
			}
			i++
			continue
		}
		if ch == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			continue
		}
		if ch == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			continue
		}
	}
	return false
}

// validateBackslashEscapedOperators detects \; \| \& \< \> outside of quotes.
// These can cause double-parse bugs when downstream code re-parses the
// normalized command string.
func validateBackslashEscapedOperators(command string) (bool, string) {
	if hasBackslashEscapedOp(command) {
		return false, "Command contains a backslash before a shell operator (;, |, &, <, >) which can hide command structure"
	}
	return true, ""
}

func hasBackslashEscapedOp(command string) bool {
	inSingleQuote := false
	inDoubleQuote := false

	for i := 0; i < len(command); i++ {
		ch := command[i]

		if ch == '\\' && !inSingleQuote {
			if !inDoubleQuote && i+1 < len(command) {
				next := command[i+1]
				if shellOperators[next] {
					return true
				}
			}
			i++
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
	}
	return false
}
