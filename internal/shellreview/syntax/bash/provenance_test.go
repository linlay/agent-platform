package bash

import "testing"

func TestArgumentProvenanceDoesNotInferLiteralFromExpandedValue(t *testing.T) {
	for _, tc := range []struct {
		word    string
		literal bool
	}{
		{`/dev/null`, true}, {`'/dev/null'`, true}, {`"/dev/null"`, true}, {`/dev/'null'`, true},
		{`"$TARGET"`, false}, {`$TARGET`, false}, {`"${TARGET}"`, false},
		{`/dev/n*`, false}, {`/dev/n?ll`, false}, {`/dev/[n]ull`, false},
		{`'/dev/n*'`, true}, {`"/dev/n*"`, true}, {`~/null`, false}, {`'~/null'`, true},
		{`$'/dev/null'`, false}, {`"$(printf /dev/null)"`, false},
	} {
		t.Run(tc.word, func(t *testing.T) {
			source := "echo 前缀 " + tc.word
			p := ParseForExecution(source, map[string]string{"TARGET": "/dev/null"})
			if p.Kind != Simple || len(p.Commands) == 0 {
				t.Fatalf("parse failed: %+v", p)
			}
			cmd := p.Commands[len(p.Commands)-1]
			if len(cmd.Argv) != len(cmd.Words) || len(cmd.Words) != 3 {
				t.Fatalf("argv/source alignment lost: %+v", cmd)
			}
			span := cmd.Words[2]
			if span.Literal != tc.literal || source[span.Start:span.End] != tc.word {
				t.Fatalf("want literal=%t and raw word %q, got %+v", tc.literal, tc.word, span)
			}
			if tc.word == `"$TARGET"` && (cmd.Argv[2] != "/dev/null" || span.Expansion) {
				t.Fatal("literal provenance must distinguish expanded /dev/null without changing connector projection")
			}
		})
	}
}

func TestRedirectTargetProvenance(t *testing.T) {
	for _, tc := range []struct {
		word    string
		target  string
		literal bool
	}{
		{`/dev/null`, "/dev/null", true}, {`'/dev/null'`, "/dev/null", true},
		{`"/dev/null"`, "/dev/null", true}, {`/dev/'null'`, "/dev/null", true},
		{`"$TARGET"`, "/dev/null", false}, {`$TARGET`, "/dev/null", false},
		{`/dev/n*`, "/dev/n*", false}, {`~/null`, "~/null", false},
		{`//dev/null`, "//dev/null", true}, {`/dev/./null`, "/dev/./null", true},
	} {
		t.Run(tc.word, func(t *testing.T) {
			source := "echo 前缀 2> " + tc.word
			p := ParseForExecution(source, map[string]string{"TARGET": "/dev/null"})
			if p.Kind != Simple || len(p.Commands) != 1 || len(p.Commands[0].Redirects) != 1 {
				t.Fatalf("parse failed: %+v", p)
			}
			r := p.Commands[0].Redirects[0]
			span := r.TargetWord
			if r.Target != tc.target || span.Literal != tc.literal || source[span.Start:span.End] != tc.word {
				t.Fatalf("target value/source lost: %+v", r)
			}
		})
	}
}

func TestBraceExpansionRetainsSharedNonliteralSource(t *testing.T) {
	source := `cat /dev/{null,zero}`
	p := ParseForExecution(source, nil)
	if p.Kind != Simple || len(p.Commands) != 1 {
		t.Fatalf("parse failed: %+v", p)
	}
	cmd := p.Commands[0]
	if len(cmd.Argv) != 3 || cmd.Argv[1] != "/dev/null" || cmd.Argv[2] != "/dev/zero" {
		t.Fatalf("brace argv changed: %+v", cmd)
	}
	for _, span := range cmd.Words[1:] {
		if span.Literal || !span.Expansion || source[span.Start:span.End] != `/dev/{null,zero}` {
			t.Fatalf("expanded arguments must retain their nonliteral source: %+v", cmd.Words)
		}
	}
}
