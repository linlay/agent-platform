package types

import "testing"

func TestSourceWordsRequireAlignedExistingSpans(t *testing.T) {
	for _, words := range [][]WordSpan{nil, {{Start: 0, End: 3, Literal: true}}} {
		sources := SourceWords(Invocation{Argv: []string{"cat", "/dev/null"}, Words: words})
		if len(sources) != 2 {
			t.Fatalf("provenance not aligned with argv: %+v", sources)
		}
		for _, source := range sources {
			if source.Literal || source.Origin != WordOriginUnknown || source.Arg != -1 {
				t.Fatalf("missing provenance acquired source trust: %+v", source)
			}
		}
	}
}
