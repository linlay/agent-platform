package shellreview

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	bashsyntax "agent-platform/internal/shellreview/syntax/bash"
	"agent-platform/internal/shellreview/types"
)

func TestExecutionPreservesWordSourcesThroughTrustedWrappers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixtures")
	}
	f := newCapabilityFixture(t, "ask_for_approval")
	vars, environment := fakeSystemPrograms(t, "env", "nice", "curl", "cat")
	for _, tc := range []struct {
		command string
		argv    []string
		raw     []string
		literal []bool
	}{
		{"curl -o /dev/null", []string{"curl", "-o", "/dev/null"}, []string{"curl", "-o", "/dev/null"}, []bool{true, true, true}},
		{`env curl -o '目录/文件.txt'`, []string{"curl", "-o", "目录/文件.txt"}, []string{"curl", "-o", `'目录/文件.txt'`}, []bool{true, true, true}},
		{`sink=/dev/null; env nice -n 1 curl -o "$sink"`, []string{"curl", "-o", "/dev/null"}, []string{"curl", "-o", `"$sink"`}, []bool{true, true, false}},
		{`sink=/dev/null; env curl "$sink" /dev/null`, []string{"curl", "/dev/null", "/dev/null"}, []string{"curl", `"$sink"`, "/dev/null"}, []bool{true, false, true}},
		{`command nice -n 1 env X=value curl --output='/dev/null'`, []string{"curl", "--output=/dev/null"}, []string{"curl", `--output='/dev/null'`}, []bool{true, true}},
		{`env -S 'curl -o /dev/null'`, []string{"curl", "-o", "/dev/null"}, []string{`'curl -o /dev/null'`, `'curl -o /dev/null'`, `'curl -o /dev/null'`}, []bool{false, false, false}},
		{`split='curl -o /dev/null'; env -S "$split"`, []string{"curl", "-o", "/dev/null"}, []string{`"$split"`, `"$split"`, `"$split"`}, []bool{false, false, false}},
		{`env --split-string='nice -n 1 curl -o' /dev/null`, []string{"curl", "-o", "/dev/null"}, []string{`--split-string='nice -n 1 curl -o'`, `--split-string='nice -n 1 curl -o'`, "/dev/null"}, []bool{false, false, true}},
		{`env -Scurl -o /dev/null`, []string{"curl", "-o", "/dev/null"}, []string{"-Scurl", "-o", "/dev/null"}, []bool{false, true, true}},
		{`env -S 'env nice curl' /dev/null`, []string{"curl", "/dev/null"}, []string{`'env nice curl'`, "/dev/null"}, []bool{false, true}},
		{`env cat /dev/nul?`, []string{"cat", "/dev/nul?"}, []string{"cat", "/dev/nul?"}, []bool{true, false}},
		{`env cat {/dev/null,/dev/null}`, []string{"cat", "/dev/null", "/dev/null"}, []string{"cat", `{/dev/null,/dev/null}`, `{/dev/null,/dev/null}`}, []bool{true, false, false}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			parsed := bashsyntax.ParseForExecution(tc.command, vars)
			if parsed.Kind != bashsyntax.Simple || len(parsed.Commands) != 1 {
				t.Fatalf("unexpected parse: %+v", parsed)
			}
			inv := parsed.Commands[0]
			before := append([]types.WordSpan(nil), inv.Words...)
			x := analyzeBashExecution(f.session, inv, f.workspace, vars, environment)
			if x.Uncertain || !reflect.DeepEqual(x.Argv, tc.argv) || len(x.Words) != len(x.Argv) {
				t.Fatalf("unexpected execution: %+v", x)
			}
			if !reflect.DeepEqual(inv.Words, before) {
				t.Fatal("wrapper changed the original projection spans")
			}
			for i, word := range x.Words {
				if word.Start < 0 || word.End > len(tc.command) || word.Start >= word.End {
					t.Fatalf("invalid source for argument %d: %+v", i, word)
				}
				if got := tc.command[word.Start:word.End]; got != tc.raw[i] || word.Literal != tc.literal[i] {
					t.Fatalf("argument %d: source %q literal=%t, want %q literal=%t", i, got, word.Literal, tc.raw[i], tc.literal[i])
				}
				if word.Arg < 0 || word.Arg >= len(inv.Argv) || inv.Words[word.Arg].Start != word.Start || inv.Words[word.Arg].End != word.End {
					t.Fatalf("argument %d lost its original argv index: %+v", i, word)
				}
				if word.Origin == types.WordOriginSplit && word.Literal {
					t.Fatalf("generated split word acquired literal trust: %+v", word)
				}
			}
		})
	}
}

func TestUntrustedWrapperKeepsItsOwnArgumentSources(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixtures")
	}
	f := newCapabilityFixture(t, "ask_for_approval")
	vars, environment := fakeSystemPrograms(t, "env", "curl")
	if err := os.WriteFile(filepath.Join(f.workspace, "env"), []byte("foreign executable\n"), 0700); err != nil {
		t.Fatal(err)
	}
	const command = "./env curl -o /dev/null"
	parsed := bashsyntax.ParseForExecution(command, vars)
	if parsed.Kind != bashsyntax.Simple || len(parsed.Commands) != 1 {
		t.Fatalf("unexpected parse: %+v", parsed)
	}
	inv := parsed.Commands[0]
	x := analyzeBashExecution(f.session, inv, f.workspace, vars, environment)
	if x.Wrapped || !x.Opaque || !reflect.DeepEqual(x.Argv, inv.Argv) || !reflect.DeepEqual(x.Words, types.SourceWords(inv)) {
		t.Fatalf("untrusted wrapper lost its invocation or sources: %+v", x)
	}
}
