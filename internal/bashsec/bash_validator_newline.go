package bashsec

import "strings"

// validateNewlines detects newlines (\n, \r) in unquoted content that could
// separate multiple commands. Allows backslash-newline continuation at word
// boundaries but flags mid-word continuations and bare newlines followed by
// non-whitespace.
func validateNewlines(fullyUnquotedPreStrip string) (bool, string) {
	if !strings.ContainsAny(fullyUnquotedPreStrip, "\n\r") {
		return true, ""
	}

	// Check for newline/CR followed by non-whitespace content.
	// Go regexp does not support lookbehinds, so we do a manual check:
	// find each \n or \r and check if it was NOT preceded by whitespace+backslash.
	if newlineWithContentRe.MatchString(fullyUnquotedPreStrip) {
		lines := strings.Split(fullyUnquotedPreStrip, "\n")
		for i := 0; i < len(lines)-1; i++ {
			line := lines[i]
			nextLine := lines[i+1]
			trimmedNext := strings.TrimSpace(nextLine)
			if trimmedNext == "" {
				continue
			}
			if len(line) >= 2 && line[len(line)-1] == '\\' && isASCIISpace(line[len(line)-2]) {
				continue
			}
			if len(line) >= 1 && line[len(line)-1] == '\\' && len(line) == 1 {
				continue
			}
			return false, "Command contains newlines that could separate multiple commands"
		}
	}

	if strings.Contains(fullyUnquotedPreStrip, "\r") {
		return false, "Command contains carriage return which could cause parsing issues"
	}

	return true, ""
}
