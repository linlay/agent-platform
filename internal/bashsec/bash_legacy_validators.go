package bashsec

import (
	"fmt"
	"regexp"
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
		// Check whether ALL such newlines are preceded by \s\\ (safe continuation).
		// Simple heuristic: check each newline position.
		lines := strings.Split(fullyUnquotedPreStrip, "\n")
		for i := 0; i < len(lines)-1; i++ {
			line := lines[i]
			nextLine := lines[i+1]
			trimmedNext := strings.TrimSpace(nextLine)
			if trimmedNext == "" {
				continue
			}
			// Check for backslash-newline continuation: line ends with \s\backslash
			if len(line) >= 2 && line[len(line)-1] == '\\' && isASCIISpace(line[len(line)-2]) {
				continue // safe continuation
			}
			if len(line) >= 1 && line[len(line)-1] == '\\' && len(line) == 1 {
				continue // just a backslash
			}
			return false, "Command contains newlines that could separate multiple commands"
		}
	}

	// Also check for \r outside double quotes (carriage return misparsing).
	if strings.Contains(fullyUnquotedPreStrip, "\r") {
		return false, "Command contains carriage return which could cause parsing issues"
	}

	return true, ""
}

// validateDangerousPatterns checks for command substitution patterns and
// unescaped backticks in the unquoted content.

func validateDangerousPatterns(unquotedContent string) (bool, string) {
	// Check for unescaped backticks.
	if hasUnescapedChar(unquotedContent, '`') {
		return false, "Command contains backticks (`) for command substitution"
	}

	// Check each command substitution pattern.
	for _, p := range commandSubstitutionPatterns {
		if p.re.MatchString(unquotedContent) {
			return false, fmt.Sprintf("Command contains %s", p.msg)
		}
	}

	return true, ""
}

// validateRedirections detects input (<) and output (>) redirection in
// unquoted content (after safe redirections like 2>&1 and >/dev/null have
// been stripped).

func validateRedirections(fullyUnquotedContent string) (bool, string) {
	if hasInputRedirection(fullyUnquotedContent) {
		return false, "Command contains input redirection (<) which could read sensitive files"
	}
	if hasOutputRedirection(fullyUnquotedContent) {
		return false, outputRedirectionReason
	}
	return true, ""
}

func hasInputRedirection(content string) bool {
	return strings.Contains(content, "<")
}

func hasOutputRedirection(content string) bool {
	return strings.Contains(content, ">")
}

func isPythonInlineCommandWithTripleQuotes(command, baseCommand string) bool {
	base := strings.TrimSpace(baseCommand)
	if base != "python" && base != "python3" {
		return false
	}
	if !pythonInlineCommandRe.MatchString(command) {
		return false
	}
	return strings.Contains(command, "'''") || strings.Contains(command, `"""`)
}

// validateIFSInjection detects usage of the IFS variable ($IFS, ${...IFS...})
// which could be used to bypass regex-based validation by altering word splitting.

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
// The TS version uses shell-quote parsing; here we check for unbalanced quotes.

func validateMalformedTokenInjection(command string) (bool, string) {
	if !strings.ContainsAny(command, ";&&||") {
		return true, "" // no command separators, no risk
	}

	// Check for unbalanced quotes (simplified from the shell-quote approach).
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
			i++ // skip escaped char
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

		// Handle backslash FIRST (before quote toggles) to avoid desync.
		if ch == '\\' && !inSingleQuote {
			if !inDoubleQuote && i+1 < len(command) {
				next := command[i+1]
				if shellOperators[next] {
					return true
				}
			}
			i++ // skip escaped char unconditionally
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

// validateBraceExpansion detects unquoted brace expansion patterns ({x,y} or
// {x..y}) that bash expands but parsers treat as literal strings. Also checks
// for quoted brace characters inside an unquoted brace context, which is an
// obfuscation attack primitive.

func validateBraceExpansion(fullyUnquotedPreStrip, originalCommand string) (bool, string) {
	content := fullyUnquotedPreStrip

	// Count unescaped braces for mismatch detection.
	var openBraces, closeBraces int
	for i := 0; i < len(content); i++ {
		if content[i] == '{' && !isEscapedAtPosition(content, i) {
			openBraces++
		} else if content[i] == '}' && !isEscapedAtPosition(content, i) {
			closeBraces++
		}
	}

	// Excess closing braces = quoted '{' was stripped, causing mismatch.
	if openBraces > 0 && closeBraces > openBraces {
		return false, "Command has excess closing braces after quote stripping, indicating possible brace expansion obfuscation"
	}

	// Check for quoted single-brace characters inside an unquoted brace context.
	if openBraces > 0 && quotedBraceRe.MatchString(originalCommand) {
		return false, "Command contains quoted brace character inside brace context (potential brace expansion obfuscation)"
	}

	// Scan for brace expansion: {x,y} or {x..y}.
	for i := 0; i < len(content); i++ {
		if content[i] != '{' || isEscapedAtPosition(content, i) {
			continue
		}

		// Find matching closing brace with nesting.
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

		// Check for comma or '..' at the outermost nesting level.
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
	// Check for \S# where the preceding two chars are not ${ (which is ${#var}).
	if hasMidWordHash(unquotedKeepQuoteChars) {
		return false, "Command contains mid-word # which is parsed differently by shell-quote vs bash"
	}

	// Also check the continuation-joined version (collapse \+\n sequences).
	joined := backslashNewlineRe.ReplaceAllStringFunc(unquotedKeepQuoteChars, func(match string) string {
		bsCount := len(match) - 1 // subtract the \n
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
		// Exclude ${# pattern.
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

		// Unquoted #: check if rest of line contains quote chars.
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
			// Skip to end of line.
			if lineEnd == -1 {
				break
			}
			i += lineEnd + 1
		}
	}

	return true, ""
}

// validateQuotedNewline detects newlines inside quoted strings where the next
// line starts with # (after trimming). Such patterns could cause line-based
// processing (like comment stripping) to drop content that bash would execute.

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

		// Newline inside quotes: check if next line would be stripped as a comment.
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

// validateZshDangerousCommands blocks Zsh-specific commands that can bypass
// security checks: zmodload, emulate, sysopen, sysread, syswrite, zpty, ztcp,
// zsocket, zf_* builtins, and fc -e.

func validateZshDangerousCommands(command string) (bool, string) {
	trimmed := strings.TrimSpace(command)
	tokens := strings.Fields(trimmed)
	baseCmd := ""
	for _, tok := range tokens {
		// Skip env var assignments (VAR=value).
		if len(tok) > 0 && isVarAssignment(tok) {
			continue
		}
		// Skip Zsh precommand modifiers.
		if zshPrecommandModifiers[tok] {
			continue
		}
		baseCmd = tok
		break
	}

	if zshDangerousCommands[baseCmd] {
		return false, fmt.Sprintf("Command uses Zsh-specific '%s' which can bypass security checks", baseCmd)
	}

	// Check fc -e (arbitrary editor execution).
	if baseCmd == "fc" {
		fcDashE := regexp.MustCompile(`\s-\S*e`)
		if fcDashE.MatchString(trimmed) {
			return false, "Command uses 'fc -e' which can execute arbitrary commands via editor"
		}
	}

	return true, ""
}

// isVarAssignment checks if a token looks like a shell variable assignment (VAR=value).

func isVarAssignment(tok string) bool {
	return varAssignRe.MatchString(tok)
}
