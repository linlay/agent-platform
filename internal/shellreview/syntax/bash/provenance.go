package bash

import (
	"strings"

	"agent-platform/internal/shellreview/types"

	"mvdan.cc/sh/v3/syntax"
)

// wordSpan records source facts independently of the expanded argv value.
// Expansion retains the connector projection contract: parameter expansion
// alone does not prevent a verified connector word from being masked.
func wordSpan(word *syntax.Word) types.WordSpan {
	if word == nil {
		return types.WordSpan{}
	}
	span := types.WordSpan{Start: int(word.Pos().Offset()), End: int(word.End().Offset()), Literal: literalWord(word)}
	syntax.Walk(word, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmExp:
			span.Expansion = true
		}
		return true
	})
	return span
}

func literalWord(word *syntax.Word) bool {
	// Brace expansion may yield several arguments, or a value with different
	// spelling even if it happens to yield just one argument.
	if syntax.SplitBraces(cloneWordParts(word)) {
		return false
	}
	for i, part := range word.Parts {
		if lit, ok := part.(*syntax.Lit); ok && i == 0 && strings.HasPrefix(lit.Value, "~") {
			return false
		}
		if !literalPart(part, false) {
			return false
		}
	}
	return true
}

func literalPart(part syntax.WordPart, quoted bool) bool {
	switch node := part.(type) {
	case *syntax.Lit:
		if quoted {
			return true
		}
		for i := 0; i < len(node.Value); i++ {
			switch node.Value[i] {
			case '\\':
				i++
			case '*', '?', '[':
				return false
			}
		}
		return true
	case *syntax.SglQuoted:
		// ANSI-C quoting interprets escapes rather than only removing quotes.
		return !node.Dollar
	case *syntax.DblQuoted:
		if node.Dollar {
			return false
		}
		for _, child := range node.Parts {
			if !literalPart(child, true) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
