package unwrap

import (
	"reflect"
	"testing"

	"agent-platform/internal/shellreview/types"
)

func TestSplitSourcesDoNotReplaceForwardedWordProvenance(t *testing.T) {
	args := []string{"-S", "cat /dev/null", "/dev/null"}
	words := types.SourceWords(types.Invocation{Argv: args, Words: []types.WordSpan{
		{Start: 0, End: 2, Literal: true},
		{Start: 3, End: 18, Literal: true},
		{Start: 19, End: 28, Literal: true},
	}})
	before := append([]types.SourceWord(nil), words...)
	expanded, ok := Execution("env", args, words, "/cwd", map[string]string{}, func(raw, cwd string) string { return raw })
	if !ok || !reflect.DeepEqual(expanded.Argv, []string{"cat", "/dev/null", "/dev/null"}) {
		t.Fatalf("unexpected split: %+v %t", expanded, ok)
	}
	for i, word := range expanded.Words {
		if i < 2 {
			if word.Arg != 1 || word.Start != 3 || word.End != 18 || word.Literal || word.Origin != types.WordOriginSplit {
				t.Fatalf("generated argument %d acquired wrong provenance: %+v", i, word)
			}
		} else if word.Arg != 2 || !word.Literal || word.Origin != types.WordOriginSyntax {
			t.Fatalf("forwarded word lost its independent provenance: %+v", word)
		}
	}
	if !reflect.DeepEqual(words, before) {
		t.Fatal("split modified the input provenance")
	}
}
