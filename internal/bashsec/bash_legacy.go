package bashsec

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func reviewBashSecurityLegacy(command string) ReviewResult {
	// 1. Control characters — must run first since null bytes confuse all other checks.
	if controlCharRe.MatchString(command) {
		return blockReview("Command contains non-printable control characters that could bypass security checks")
	}

	// 2. Build validation context: extract quoted/unquoted content.
	baseCommand := command
	if idx := strings.IndexAny(command, " \t"); idx >= 0 {
		baseCommand = command[:idx]
	}
	baseCommand = strings.TrimSpace(baseCommand)

	extracted := extractQuotedContent(command)

	fullyUnquotedPreStrip := extracted.fullyUnquoted
	fullyUnquotedContent := stripSafeRedirections(extracted.fullyUnquoted)
	unquotedContent := extracted.withDoubleQuotes // single-quote content stripped, double-quote content kept
	unquotedKeepQuoteChars := extracted.unquotedKeepQuoteChars

	// 3. Run all validators in order.
	type validator struct {
		name string
		fn   func() (bool, string)
	}

	validators := []validator{
		{"incomplete_commands", func() (bool, string) {
			return validateIncompleteCommands(command)
		}},
		{"obfuscated_flags", func() (bool, string) {
			return validateObfuscatedFlags(command, baseCommand, fullyUnquotedContent)
		}},
		{"shell_metacharacters", func() (bool, string) {
			return validateShellMetacharacters(command) // uses raw command because regexes match quoted patterns
		}},
		{"dangerous_variables", func() (bool, string) {
			return validateDangerousVariables(fullyUnquotedContent)
		}},
		{"comment_quote_desync", func() (bool, string) {
			return validateCommentQuoteDesync(command)
		}},
		{"quoted_newline", func() (bool, string) {
			return validateQuotedNewline(command)
		}},
		{"newlines", func() (bool, string) {
			return validateNewlines(fullyUnquotedPreStrip)
		}},
		{"ifs_injection", func() (bool, string) {
			return validateIFSInjection(command)
		}},
		{"proc_environ_access", func() (bool, string) {
			return validateProcEnvironAccess(command)
		}},
		{"command_substitution", func() (bool, string) {
			return validateDangerousPatterns(unquotedContent)
		}},
		{"redirections", func() (bool, string) {
			return validateRedirections(fullyUnquotedContent)
		}},
		{"backslash_escaped_whitespace", func() (bool, string) {
			return validateBackslashEscapedWhitespace(command)
		}},
		{"backslash_escaped_operators", func() (bool, string) {
			return validateBackslashEscapedOperators(command)
		}},
		{"unicode_whitespace", func() (bool, string) {
			return validateUnicodeWhitespace(command)
		}},
		{"mid_word_hash", func() (bool, string) {
			return validateMidWordHash(unquotedKeepQuoteChars)
		}},
		{"brace_expansion", func() (bool, string) {
			return validateBraceExpansion(fullyUnquotedPreStrip, command)
		}},
		{"zsh_dangerous_commands", func() (bool, string) {
			return validateZshDangerousCommands(command)
		}},
		{"malformed_token_injection", func() (bool, string) {
			return validateMalformedTokenInjection(command)
		}},
	}

	for _, v := range validators {
		if ok, reason := v.fn(); !ok {
			if canApproveBashSecurityFailure(v.name, command, baseCommand, fullyUnquotedContent, reason) {
				return approvalReviewForValidator(command, reason, v.name)
			}
			return blockReview(reason)
		}
	}

	return ReviewResult{Decision: ReviewAllow}
}

func blockReview(reason string) ReviewResult {
	return ReviewResult{Decision: ReviewBlock, Reason: reason}
}

func ApprovalFingerprint(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:])
}

func canApproveBashSecurityFailure(name, command, baseCommand, fullyUnquotedContent, reason string) bool {
	switch name {
	case "redirections":
		return reason == outputRedirectionReason &&
			hasOutputRedirection(fullyUnquotedContent) &&
			!hasInputRedirection(fullyUnquotedContent)
	case "quoted_newline":
		return hasOutputRedirection(fullyUnquotedContent) &&
			!hasInputRedirection(fullyUnquotedContent)
	case "obfuscated_flags":
		return reason == tripleQuoteReason && isPythonInlineCommandWithTripleQuotes(command, baseCommand)
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Helper types and functions
// ---------------------------------------------------------------------------

// quoteExtraction holds the results of extracting quoted content from a command.

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
			keepQC.WriteByte(ch) // preserve quote char
			continue
		}

		if ch == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			keepQC.WriteByte(ch) // preserve quote char
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

func hasUnescapedChar(content string, ch byte) bool {
	i := 0
	for i < len(content) {
		if content[i] == '\\' && i+1 < len(content) {
			i += 2 // skip backslash and next char
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

// ---------------------------------------------------------------------------
// Compiled regular expressions
// ---------------------------------------------------------------------------
