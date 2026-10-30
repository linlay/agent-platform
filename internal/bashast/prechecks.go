package bashast

import (
	"regexp"
	"strings"

	"agent-platform/internal/shellreview/syntax"

	shsyntax "mvdan.cc/sh/v3/syntax"
)

var (
	zshTildeBracketRe      = regexp.MustCompile(`~\[`)
	zshEqualsExpansionRe   = regexp.MustCompile(`(?:^|[\s;&|])=[A-Za-z_]`)
	backslashWhitespaceMsg = "Command contains backslash-escaped whitespace that could alter command parsing"
)

const (
	zshTildeBracketReason    = "Command contains Zsh-style parameter expansion"
	zshEqualsExpansionReason = "Command contains Zsh equals expansion"
)

func runPrechecks(command string) (bool, string) {
	if reason := CheckRawText(command); reason != "" {
		return false, reason
	}
	switch {
	case hasBackslashEscapedWhitespace(command):
		return false, backslashWhitespaceMsg
	case zshTildeBracketRe.MatchString(command):
		return false, zshTildeBracketReason
	case zshEqualsExpansionRe.MatchString(command):
		return false, zshEqualsExpansionReason
	default:
		return true, ""
	}
}

// CheckRawText applies the grammar-independent guard to the whole command and
// rejects non-ASCII whitespace or invisible formatting only outside quotes;
// quoted data such as emoji joiners or CJK spaces cannot change word splitting.
// Nested command substitutions have their own quoting context. Heredoc bodies
// and comments remain unquoted text; malformed syntax gets no quote exemption.
func CheckRawText(command string) string {
	if reason := syntax.CheckText(command); reason != "" {
		return reason
	}
	// Ordinary commands need no extra parse. Only literal hidden characters
	// require determining which source spans are quoted data.
	hidden := syntax.CheckHidden(command)
	if hidden == "" {
		return ""
	}
	file, err := parseFile(command)
	if err != nil || file == nil {
		return hidden
	}
	unquoted := []byte(command)
	mask := func(node shsyntax.Node) {
		for i := node.Pos().Offset(); i < node.End().Offset(); i++ {
			unquoted[i] = ' '
		}
	}
	quoted := false
	var parents []bool
	shsyntax.Walk(file, func(node shsyntax.Node) bool {
		if node == nil {
			quoted = parents[len(parents)-1]
			parents = parents[:len(parents)-1]
			return true
		}
		previous := quoted
		switch node.(type) {
		case *shsyntax.SglQuoted:
			mask(node)
			return false
		case *shsyntax.DblQuoted:
			quoted = true
		case *shsyntax.CmdSubst, *shsyntax.ProcSubst:
			quoted = false
		case *shsyntax.Lit:
			if quoted {
				mask(node)
			}
		}
		parents = append(parents, previous)
		return true
	})
	return syntax.CheckHidden(string(unquoted))
}

func hasBackslashEscapedWhitespace(command string) bool {
	inSingleQuote := false
	inDoubleQuote := false
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && !inSingleQuote {
			if !inDoubleQuote && i+1 < len(runes) && (runes[i+1] == ' ' || runes[i+1] == '\t') {
				return true
			}
			i++
			continue
		}
		if r == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			continue
		}
		if r == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			continue
		}
	}
	return false
}

func IsHardBlockReason(reason string) bool {
	reason = strings.TrimSpace(reason)
	if syntax.IsTextRejection(reason) {
		return true
	}
	switch reason {
	case zshTildeBracketReason,
		zshEqualsExpansionReason:
		// Backslash-escaped whitespace remains unparsed by the reviewer and is
		// approval-only; brace expansion is expanded by the walker.
		return true
	default:
		return false
	}
}
