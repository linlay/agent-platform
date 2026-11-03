package unwrap

import "agent-platform/internal/shellreview/types"

// Expansion keeps argv and its source words aligned after one trusted wrapper.
type Expansion struct {
	Argv  []string
	Words []types.SourceWord
	Cwd   string
}

// argumentRef maps a result to an input index without comparing its value.
type argumentRef struct {
	arg       int
	generated bool
}

func suffixRefs(start, count int) []argumentRef {
	refs := make([]argumentRef, 0, count-start)
	for i := start; i < count; i++ {
		refs = append(refs, argumentRef{arg: i})
	}
	return refs
}

func mapWords(refs []argumentRef, words []types.SourceWord, count int) []types.SourceWord {
	if len(refs) == 0 {
		return nil
	}
	out := make([]types.SourceWord, len(refs))
	for i, ref := range refs {
		out[i].Arg = -1
		if len(words) == count && ref.arg >= 0 && ref.arg < len(words) {
			out[i] = words[ref.arg]
		}
		if ref.generated {
			out[i].Literal = false
			out[i].Origin = types.WordOriginSplit
		}
	}
	return out
}

func forward(args []string, words []types.SourceWord, start int, cwd string) Expansion {
	return Expansion{Argv: args[start:], Words: mapWords(suffixRefs(start, len(args)), words, len(args)), Cwd: cwd}
}
