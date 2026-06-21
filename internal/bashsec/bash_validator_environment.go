package bashsec

import "strings"

// validateIFSInjection detects usage of the IFS variable ($IFS, ${...IFS...})
// which could be used to bypass validation by altering word splitting.
func validateIFSInjection(command string) (bool, string) {
	if ifsRe.MatchString(command) {
		return false, "Command contains IFS variable usage which could bypass security validation"
	}
	return true, ""
}

// validateProcEnvironAccess blocks access to /proc/*/environ which could
// expose sensitive environment variables like API keys.
func validateProcEnvironAccess(command string) (bool, string) {
	if procEnvironRe.MatchString(command) {
		return false, "Command accesses /proc/*/environ which could expose sensitive environment variables"
	}
	return true, ""
}

// validateMalformedTokenInjection is a simplified check for unbalanced quotes
// that could indicate injection via malformed shell tokens.
func validateMalformedTokenInjection(command string) (bool, string) {
	if !strings.ContainsAny(command, ";&&||") {
		return true, ""
	}

	singleCount := 0
	doubleCount := 0
	escaped := false
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '\'' {
			singleCount++
		} else if ch == '"' {
			doubleCount++
		}
	}
	if singleCount%2 != 0 || doubleCount%2 != 0 {
		return false, "Command contains ambiguous syntax with unbalanced quotes that could be misinterpreted"
	}
	return true, ""
}
