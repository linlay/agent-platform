package bashsec

import (
	"strings"

	"agent-platform/internal/bashast"
)

func reviewLegacyCompatibleWithAST(command string, result bashast.ParseResult) ReviewResult {
	legacyCommand := maskHeredocBodiesForLegacy(command, result)
	if controlCharRe.MatchString(legacyCommand) {
		return blockReview("Command contains non-printable control characters that could bypass security checks")
	}

	baseCommand := legacyCommand
	if idx := strings.IndexAny(legacyCommand, " \t"); idx >= 0 {
		baseCommand = legacyCommand[:idx]
	}
	baseCommand = strings.TrimSpace(baseCommand)

	extracted := extractQuotedContent(legacyCommand)
	fullyUnquotedPreStrip := extracted.fullyUnquoted
	fullyUnquotedContent := stripSafeRedirections(extracted.fullyUnquoted)
	unquotedKeepQuoteChars := extracted.unquotedKeepQuoteChars

	validators := []struct {
		name string
		fn   func() (bool, string)
	}{
		{"incomplete_commands", func() (bool, string) {
			return validateIncompleteCommands(legacyCommand)
		}},
		{"obfuscated_flags", func() (bool, string) {
			return validateObfuscatedFlags(legacyCommand, baseCommand, fullyUnquotedContent)
		}},
		{"shell_metacharacters", func() (bool, string) {
			return validateShellMetacharactersFromAST(result)
		}},
		{"comment_quote_desync", func() (bool, string) {
			return validateCommentQuoteDesync(legacyCommand)
		}},
		{"quoted_newline", func() (bool, string) {
			return validateQuotedNewline(legacyCommand)
		}},
		{"newlines", func() (bool, string) {
			return validateNewlines(fullyUnquotedPreStrip)
		}},
		{"ifs_injection", func() (bool, string) {
			return validateIFSInjection(legacyCommand)
		}},
		{"proc_environ_access", func() (bool, string) {
			return validateProcEnvironAccess(legacyCommand)
		}},
		{"backslash_escaped_whitespace", func() (bool, string) {
			return validateBackslashEscapedWhitespace(legacyCommand)
		}},
		{"unicode_whitespace", func() (bool, string) {
			return validateUnicodeWhitespace(legacyCommand)
		}},
		{"mid_word_hash", func() (bool, string) {
			return validateMidWordHash(unquotedKeepQuoteChars)
		}},
		{"brace_expansion", func() (bool, string) {
			return validateBraceExpansion(fullyUnquotedPreStrip, legacyCommand)
		}},
		{"zsh_dangerous_commands", func() (bool, string) {
			return validateZshDangerousCommands(legacyCommand)
		}},
		{"malformed_token_injection", func() (bool, string) {
			return validateMalformedTokenInjection(legacyCommand)
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

func validateShellMetacharactersFromAST(result bashast.ParseResult) (bool, string) {
	for _, cmd := range result.Commands {
		for _, argv := range deterministicCommandChain(cmd.Argv) {
			if findPatternArgvHasShellMetacharacters(argv) {
				return false, shellMetacharactersReason
			}
		}
	}
	return true, ""
}

func findPatternArgvHasShellMetacharacters(argv []string) bool {
	if len(argv) == 0 || normalizedCommandBase(argv[0]) != "find" {
		return false
	}
	for idx := 1; idx < len(argv); idx++ {
		arg := argv[idx]
		switch arg {
		case "-name", "-path", "-iname":
			if idx+1 < len(argv) && strings.ContainsAny(argv[idx+1], ";|&") {
				return true
			}
			idx++
		case "-regex":
			if idx+1 < len(argv) && strings.ContainsAny(argv[idx+1], ";&") {
				return true
			}
			idx++
		}
	}
	return false
}

func maskHeredocBodiesForLegacy(command string, result bashast.ParseResult) string {
	var masked []byte
	for _, cmd := range result.Commands {
		for _, redirect := range cmd.Redirects {
			start, end, ok := heredocLegacyMaskRange(command, redirect)
			if !ok {
				continue
			}
			if masked == nil {
				masked = []byte(command)
			}
			for idx := start; idx < end; idx++ {
				masked[idx] = ' '
			}
		}
	}
	if masked == nil {
		return command
	}
	return string(masked)
}

func heredocLegacyMaskRange(command string, redirect bashast.Redirect) (int, int, bool) {
	if !redirect.IsHeredoc {
		return 0, 0, false
	}
	start := redirect.HeredocBodyStart
	end := redirect.HeredocBodyEnd
	if start < 0 || end < start || start > len(command) {
		return 0, 0, false
	}
	if end > len(command) {
		end = len(command)
	}

	maskStart := start
	if idx := strings.LastIndexByte(command[:start], '\n'); idx >= 0 {
		maskStart = idx
		if maskStart > 0 && command[maskStart-1] == '\r' {
			maskStart--
		}
	}

	maskEnd := end
	if end < len(command) {
		if idx := strings.IndexByte(command[end:], '\n'); idx >= 0 {
			maskEnd = end + idx + 1
		} else {
			maskEnd = len(command)
		}
	}
	if maskEnd < maskStart {
		return 0, 0, false
	}
	return maskStart, maskEnd, true
}
