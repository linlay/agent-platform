package bashsec

// validateBraceExpansion detects unquoted brace expansion patterns ({x,y} or
// {x..y}) that bash expands but parsers treat as literal strings. It also
// checks for quoted brace characters inside an unquoted brace context.
func validateBraceExpansion(fullyUnquotedPreStrip, originalCommand string) (bool, string) {
	content := fullyUnquotedPreStrip

	var openBraces, closeBraces int
	for i := 0; i < len(content); i++ {
		if content[i] == '{' && !isEscapedAtPosition(content, i) {
			openBraces++
		} else if content[i] == '}' && !isEscapedAtPosition(content, i) {
			closeBraces++
		}
	}

	if openBraces > 0 && closeBraces > openBraces {
		return false, "Command has excess closing braces after quote stripping, indicating possible brace expansion obfuscation"
	}

	if openBraces > 0 && quotedBraceRe.MatchString(originalCommand) {
		return false, "Command contains quoted brace character inside brace context (potential brace expansion obfuscation)"
	}

	for i := 0; i < len(content); i++ {
		if content[i] != '{' || isEscapedAtPosition(content, i) {
			continue
		}

		depth := 1
		matchingClose := -1
		for j := i + 1; j < len(content); j++ {
			if content[j] == '{' && !isEscapedAtPosition(content, j) {
				depth++
			} else if content[j] == '}' && !isEscapedAtPosition(content, j) {
				depth--
				if depth == 0 {
					matchingClose = j
					break
				}
			}
		}
		if matchingClose == -1 {
			continue
		}

		innerDepth := 0
		for k := i + 1; k < matchingClose; k++ {
			if content[k] == '{' && !isEscapedAtPosition(content, k) {
				innerDepth++
			} else if content[k] == '}' && !isEscapedAtPosition(content, k) {
				innerDepth--
			} else if innerDepth == 0 {
				if content[k] == ',' {
					return false, "Command contains brace expansion that could alter command parsing"
				}
				if content[k] == '.' && k+1 < matchingClose && content[k+1] == '.' {
					return false, "Command contains brace expansion that could alter command parsing"
				}
			}
		}
	}

	return true, ""
}
