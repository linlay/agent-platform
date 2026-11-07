package bash

import (
	"regexp"
	"strings"

	"agent-platform/internal/commandtext"

	shsyntax "mvdan.cc/sh/v3/syntax"
)

var (
	zshTildeBracketRe    = regexp.MustCompile(`~\[`)
	zshEqualsExpansionRe = regexp.MustCompile(`(?:^|[\s;&|])=[A-Za-z_]`)
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
	if reason := commandtext.CheckText(command); reason != "" {
		return reason
	}
	// Ordinary commands need no extra parse. Only literal hidden characters
	// require determining which source spans are quoted data.
	hidden := commandtext.CheckHidden(command)
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
	return commandtext.CheckHidden(string(unquoted))
}

func IsHardBlockReason(reason string) bool {
	reason = strings.TrimSpace(reason)
	if commandtext.IsTextRejection(reason) {
		return true
	}
	switch reason {
	case zshTildeBracketReason,
		zshEqualsExpansionReason:
		// Zsh-only spellings are refused outright; brace expansion and
		// backslash escapes are resolved by the walker, so they need none.
		return true
	default:
		return false
	}
}
