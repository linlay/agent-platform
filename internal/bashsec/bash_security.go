package bashsec

import (
	"regexp"
	"strings"

	"agent-platform/internal/bashast"
)

type ReviewDecision string

const (
	ReviewAllow            ReviewDecision = "allow"
	ReviewRequiresApproval ReviewDecision = "requires_approval"
	ReviewBlock            ReviewDecision = "block"
)

type ReviewResult struct {
	Decision    ReviewDecision
	Reason      string
	Fingerprint string
	RuleKey     string
	Level       int
}

const (
	RuleKeyRedirections               = "bashsec:redirections"
	RuleKeyQuotedNewline              = "bashsec:quoted_newline"
	RuleKeyObfuscatedFlagsTripleQuote = "bashsec:obfuscated_flags:triple_quote"
	RuleKeyTooComplex                 = "bashast:too_complex"
	RuleKeyRuntimeWrapperXargs        = "bashsec:runtime_wrapper:xargs"
	RuleKeyRuntimeWrapperFindExec     = "bashsec:runtime_wrapper:find_exec"

	LevelRedirections               = 2
	LevelQuotedNewline              = 2
	LevelObfuscatedFlagsTripleQuote = 3
	LevelTooComplex                 = 4
	LevelRuntimeWrapper             = 3
)

func ReviewBashSecurity(command string) ReviewResult {
	return ReviewBashSecurityWithKnownVariables(command, nil)
}

func ReviewBashSecurityWithKnownVariables(command string, variables map[string]string) ReviewResult {
	astResult, embeddedScripts := bashast.ParseWithEmbeddedDetectionAndKnownVariables(command, variables)
	switch astResult.Kind {
	case bashast.Simple:
		return reviewFromAST(command, astResult, embeddedScripts)
	case bashast.TooComplex:
		if bashast.IsHardBlockReason(astResult.Reason) {
			return blockReview(astResult.Reason)
		}
		legacy := reviewBashSecurityLegacy(command)
		if legacy.Decision != ReviewAllow {
			return legacy
		}
		reason := strings.TrimSpace(astResult.Reason)
		if reason == "" {
			reason = "Command is too complex for static AST security analysis"
		}
		return ReviewResult{
			Decision:    ReviewRequiresApproval,
			Reason:      reason,
			Fingerprint: ApprovalFingerprint(command),
			RuleKey:     RuleKeyTooComplex,
			Level:       LevelTooComplex,
		}
	case bashast.ParseUnavailable:
		return reviewBashSecurityLegacy(command)
	default:
		return ReviewResult{
			Decision:    ReviewRequiresApproval,
			Reason:      "Command could not be classified by AST security analysis",
			Fingerprint: ApprovalFingerprint(command),
			RuleKey:     RuleKeyTooComplex,
			Level:       LevelTooComplex,
		}
	}
}

var astDangerousCommands = map[string]bool{
	".":         true,
	"alias":     true,
	"bg":        true,
	"builtin":   false,
	"complete":  true,
	"compgen":   true,
	"compopt":   true,
	"coproc":    true,
	"enable":    true,
	"eval":      true,
	"exec":      true,
	"fg":        true,
	"hash":      true,
	"jobs":      true,
	"mapfile":   true,
	"read":      true,
	"readarray": true,
	"set":       true,
	"shopt":     true,
	"source":    true,
	"trap":      true,
	"unalias":   true,
	"unset":     true,
}

var (
	safeRedir2To1         = regexp.MustCompile(`\s+2\s*>&\s*1(?:\s|$)`)
	safeRedirDevNull      = regexp.MustCompile(`[012]?\s*>\s*/dev/null(?:\s|$)`)
	safeRedirInputDevNull = regexp.MustCompile(`\s*<\s*/dev/null(?:\s|$)`)
)

// hasUnescapedChar checks whether content contains an unescaped occurrence of
// the given single byte. Backslash-escaped characters are skipped.
var (
	// Control characters: 0x00-0x08, 0x0B-0x0C, 0x0E-0x1F, 0x7F.
	// Excludes tab (0x09), newline (0x0A), carriage return (0x0D).
	controlCharRe = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")

	// Incomplete command patterns.
	startsWithTabRe      = regexp.MustCompile(`^\s*\t`)
	startsWithOperatorRe = regexp.MustCompile(`^\s*(&&|\|\||;|>>?|<)`)

	// Obfuscated flags.
	ansiCQuoteRe        = regexp.MustCompile(`\$'[^']*'`)
	localeQuoteRe       = regexp.MustCompile(`\$"[^"]*"`)
	emptySpecialQuoteRe = regexp.MustCompile(`\$['"]{2}\s*-`)
	emptyQuotePairRe    = regexp.MustCompile(`(?:^|\s)(?:''|"")+\s*-`)
	emptyQuoteAdjRe     = regexp.MustCompile(`(?:""|'')+['"]-`)
	threeConsecQuotesRe = regexp.MustCompile(`(?:^|\s)['"]{3,}`)
	quotedDashInUQRe    = regexp.MustCompile(`\s['"\x60]-`)
	doubleQuoteDashRe   = regexp.MustCompile(`['"\x60]{2}-`)

	// Shell metacharacter patterns.
	metaInQuotesRe  = regexp.MustCompile(`(?:^|\s)["'][^"']*[;&][^"']*["'](?:\s|$)`)
	findNameMetaRe  = regexp.MustCompile(`-name\s+["'][^"']*[;|&][^"']*["']`)
	findPathMetaRe  = regexp.MustCompile(`-path\s+["'][^"']*[;|&][^"']*["']`)
	findInameMetaRe = regexp.MustCompile(`-iname\s+["'][^"']*[;|&][^"']*["']`)
	findRegexMetaRe = regexp.MustCompile(`-regex\s+["'][^"']*[;&][^"']*["']`)

	// Dangerous variables in pipe/redirect context.
	varAfterRedirRe  = regexp.MustCompile(`[<>|]\s*\$[A-Za-z_]`)
	varBeforeRedirRe = regexp.MustCompile(`\$[A-Za-z_][A-Za-z0-9_]*\s*[|<>]`)

	// Newline detection — newline/CR followed by non-whitespace, except
	// backslash-newline continuations at word boundaries.
	newlineWithContentRe = regexp.MustCompile(`[\n\r]\s*\S`)

	// IFS injection.
	ifsRe = regexp.MustCompile(`\$IFS|\$\{[^}]*IFS`)

	// /proc/*/environ access.
	procEnvironRe = regexp.MustCompile(`/proc/.*/environ`)

	// Unicode whitespace characters.
	unicodeWsRe = regexp.MustCompile("[\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000\uFEFF]")

	// Quoted brace patterns (attack primitive for brace expansion obfuscation).
	quotedBraceRe = regexp.MustCompile(`['"][{}]['"]`)

	pythonInlineCommandRe = regexp.MustCompile(`(?s)(?:^|\s)(?:python|python3)\s+(?:[^\s]+\s+)*-c(?:\s|=)`)
)

// Command substitution patterns — each has a regex and a description.
var commandSubstitutionPatterns = []struct {
	re  *regexp.Regexp
	msg string
}{
	{regexp.MustCompile(`<\(`), "process substitution <()"},
	{regexp.MustCompile(`>\(`), "process substitution >()"},
	{regexp.MustCompile(`=\(`), "Zsh process substitution =()"},
	{regexp.MustCompile(`(?:^|[\s;&|])=[a-zA-Z_]`), "Zsh equals expansion (=cmd)"},
	{regexp.MustCompile(`\$\(`), "$() command substitution"},
	{regexp.MustCompile(`\$\{`), "${} parameter substitution"},
	{regexp.MustCompile(`\$\[`), "$[] legacy arithmetic expansion"},
	{regexp.MustCompile(`~\[`), "Zsh-style parameter expansion"},
	{regexp.MustCompile(`\(e:`), "Zsh-style glob qualifiers"},
	{regexp.MustCompile(`\(\+`), "Zsh glob qualifier with command execution"},
	{regexp.MustCompile(`\}\s*always\s*\{`), "Zsh always block (try/always construct)"},
	{regexp.MustCompile(`<#`), "PowerShell comment syntax"},
}

// Zsh dangerous commands that can bypass security checks.
var zshDangerousCommands = map[string]bool{
	"zmodload": true,
	"emulate":  true,
	"sysopen":  true,
	"sysread":  true,
	"syswrite": true,
	"sysseek":  true,
	"zpty":     true,
	"ztcp":     true,
	"zsocket":  true,
	"mapfile":  true,
	"zf_rm":    true,
	"zf_mv":    true,
	"zf_ln":    true,
	"zf_chmod": true,
	"zf_chown": true,
	"zf_mkdir": true,
	"zf_rmdir": true,
	"zf_chgrp": true,
}

// Zsh precommand modifiers that are skipped when finding the base command.
var zshPrecommandModifiers = map[string]bool{
	"command":   true,
	"builtin":   true,
	"noglob":    true,
	"nocorrect": true,
}

// Shell operators relevant for backslash-escaped operator detection.
var shellOperators = map[byte]bool{
	';': true,
	'|': true,
	'&': true,
	'<': true,
	'>': true,
}

// ---------------------------------------------------------------------------
// Validators
// ---------------------------------------------------------------------------

// validateIncompleteCommands rejects commands that look like fragments:
// starting with tab, flags (dash), or shell operators.
var (
	hasFlagPrefix      = regexp.MustCompile(`^-+[a-zA-Z0-9$` + "`" + `]`)
	allDashesRe        = regexp.MustCompile(`^-+$`)
	flagContinuationRe = regexp.MustCompile(`[a-zA-Z0-9\\$` + "`" + `{-]`)
)

const (
	outputRedirectionReason = "Command contains output redirection (>) which could write to arbitrary files"
	tripleQuoteReason       = "Command contains consecutive quote characters at word start (potential obfuscation)"
)

var backslashNewlineRe = regexp.MustCompile(`\\+\n`)

// hasMidWordHash checks if content has a # preceded by a non-whitespace
// character, excluding the ${# pattern (bash string-length syntax).
var varAssignRe = regexp.MustCompile(`^[A-Za-z_]\w*=`)
