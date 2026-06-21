package bashsec

import (
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
