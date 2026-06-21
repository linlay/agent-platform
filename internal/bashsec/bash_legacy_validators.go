package bashsec

import (
	"strings"
)

func validateIncompleteCommands(command string) (bool, string) {
	trimmed := strings.TrimSpace(command)

	if startsWithTabRe.MatchString(command) {
		return false, "Command appears to be an incomplete fragment (starts with tab)"
	}
	if strings.HasPrefix(trimmed, "-") {
		return false, "Command appears to be an incomplete fragment (starts with flags)"
	}
	if startsWithOperatorRe.MatchString(command) {
		return false, "Command appears to be a continuation line (starts with operator)"
	}
	return true, ""
}

// validateObfuscatedFlags detects shell quoting tricks used to hide flags
// from regex-based blocklists: ANSI-C quoting ($'...'), locale quoting ($"..."),
// empty quote chains, and quoted characters in flag names.

func validateObfuscatedFlags(command, baseCommand, fullyUnquotedContent string) (bool, string) {
	// Echo without shell operators is safe for obfuscated flags.
	hasShellOps := strings.ContainsAny(command, "|&;")
	if baseCommand == "echo" && !hasShellOps {
		return true, ""
	}

	// 1. ANSI-C quoting: $'...'
	if ansiCQuoteRe.MatchString(command) {
		return false, "Command contains ANSI-C quoting which can hide characters"
	}

	// 2. Locale quoting: $"..."
	if localeQuoteRe.MatchString(command) {
		return false, "Command contains locale quoting which can hide characters"
	}

	// 3. Empty ANSI-C or locale quotes followed by dash: $''-exec, $""-exec
	if emptySpecialQuoteRe.MatchString(command) {
		return false, "Command contains empty special quotes before dash (potential bypass)"
	}

	// 4. Empty quote pairs followed by dash: ''-exec, ""-exec
	if emptyQuotePairRe.MatchString(command) {
		return false, "Command contains empty quotes before dash (potential bypass)"
	}

	// 4b. Empty quote pair adjacent to quoted dash: """-f"
	if emptyQuoteAdjRe.MatchString(command) {
		return false, "Command contains empty quote pair adjacent to quoted dash (potential flag obfuscation)"
	}

	// 4c. 3+ consecutive quotes at word start.
	if threeConsecQuotesRe.MatchString(command) {
		return false, tripleQuoteReason
	}

	// 5. Scan for quoted characters inside flag names (simplified from TS).
	// Check for whitespace+quote+dash patterns and dash+quote patterns in
	// the unquoted content.
	if quotedDashInUQRe.MatchString(fullyUnquotedContent) {
		return false, "Command contains quoted characters in flag names"
	}
	if doubleQuoteDashRe.MatchString(fullyUnquotedContent) {
		return false, "Command contains quoted characters in flag names"
	}

	// 6. Character-by-character scan for obfuscated flags (simplified).
	// Look for whitespace followed by a quote containing a dash-prefixed flag.
	if detectObfuscatedFlagScan(command) {
		return false, "Command contains quoted characters in flag names"
	}

	return true, ""
}

// detectObfuscatedFlagScan performs a character-by-character scan for
// flag obfuscation via quoting: e.g., "-exec" or "-"exec.

func detectObfuscatedFlagScan(command string) bool {
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	for i := 0; i < len(command)-1; i++ {
		cur := command[i]
		next := command[i+1]

		if escaped {
			escaped = false
			continue
		}
		if cur == '\\' && !inSingleQuote {
			escaped = true
			continue
		}
		if cur == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			continue
		}
		if cur == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			continue
		}

		// Only inspect flags outside quotes.
		if inSingleQuote || inDoubleQuote {
			continue
		}

		// Whitespace followed by a quote: check if the quoted content is a flag.
		if isASCIISpace(cur) && (next == '\'' || next == '"' || next == '`') {
			quoteChar := next
			j := i + 2
			var inside strings.Builder
			for j < len(command) && command[j] != quoteChar {
				inside.WriteByte(command[j])
				j++
			}
			if j < len(command) && command[j] == quoteChar {
				content := inside.String()
				// Flag chars inside: "-exec", "--flag"
				if hasFlagPrefix.MatchString(content) {
					return true
				}
				// Split-quote flag: "-"exec (dashes inside, letters continue)
				if allDashesRe.MatchString(content) && j+1 < len(command) && flagContinuationRe.Match([]byte{command[j+1]}) {
					return true
				}
			}
		}

		// Whitespace followed by dash followed by quote: dash"-"flag
		if isASCIISpace(cur) && next == '-' {
			j := i + 1
			var flagContent strings.Builder
			for j < len(command) {
				fc := command[j]
				if isASCIISpace(fc) || fc == '=' {
					break
				}
				flagContent.WriteByte(fc)
				j++
			}
			fc := flagContent.String()
			if strings.ContainsAny(fc, "'\"") {
				return true
			}
		}
	}
	return false
}

func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// validateShellMetacharacters detects ;, |, & in patterns that could indicate
// metacharacter injection in arguments (e.g., inside find -name patterns).

func validateShellMetacharacters(unquotedContent string) (bool, string) {
	if metaInQuotesRe.MatchString(unquotedContent) {
		return false, shellMetacharactersReason
	}
	if findNameMetaRe.MatchString(unquotedContent) ||
		findPathMetaRe.MatchString(unquotedContent) ||
		findInameMetaRe.MatchString(unquotedContent) {
		return false, shellMetacharactersReason
	}
	if findRegexMetaRe.MatchString(unquotedContent) {
		return false, shellMetacharactersReason
	}
	return true, ""
}

// validateDangerousVariables detects variable references ($VAR) adjacent to
// pipes or redirects in unquoted content — these could expand to dangerous
// commands or paths.

func validateDangerousVariables(fullyUnquotedContent string) (bool, string) {
	if varAfterRedirRe.MatchString(fullyUnquotedContent) ||
		varBeforeRedirRe.MatchString(fullyUnquotedContent) {
		return false, "Command contains variables in dangerous contexts (redirections or pipes)"
	}
	return true, ""
}
