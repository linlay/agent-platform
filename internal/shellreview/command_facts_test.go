package shellreview

import (
	"agent-platform/internal/shellreview/commands"
	"agent-platform/internal/shellreview/dialect"
	bashsyntax "agent-platform/internal/shellreview/syntax/bash"
	"agent-platform/internal/shellreview/types"
	"runtime"
	"testing"
)

func TestCommandValueProvenanceSurvivesWrappersAndOptions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixtures")
	}
	f := newCapabilityFixture(t, "ask_for_approval")
	vars, environment := fakeSystemPrograms(t, "curl", "env", "nice", "jq", "find", "sed", "pdftotext", "tar")
	for _, tc := range []struct {
		command, path, raw string
		literal            bool
	}{
		{`env nice -n 1 curl --output='/dev/null' https://x`, "/dev/null", "/dev/null", true},
		{`curl -so目录/文件.txt https://x`, "目录/文件.txt", "目录/文件.txt", true},
		{`sink=/dev/null; curl -o "$sink" https://x`, "/dev/null", "", false},
		{`env -S 'curl -o /dev/null'`, "/dev/null", "", false},
		{`env -S 'curl -o' /dev/null`, "/dev/null", "/dev/null", true},
		{`sink=/dev/null; curl --output="$sink" --output=/dev/null https://x`, "/dev/null", "", false},
		{`jq --rawfile key '目录/文件.txt' -n .`, "目录/文件.txt", "目录/文件.txt", true},
		{`find . -fprint '目录/文件.txt'`, "目录/文件.txt", "目录/文件.txt", true},
		{`sed -f'规则.sed' input`, "规则.sed", "", false}, // unsupported attached form remains unknown
		{`pdftotext -layout in.pdf '目录/文件.txt'`, "目录/文件.txt", "目录/文件.txt", true},
		{`tar -cf '目录/文件.tar' input`, "目录/文件.tar", "目录/文件.tar", true},
	} {
		p := bashsyntax.ParseForExecution(tc.command, vars)
		if p.Kind != bashsyntax.Simple || len(p.Commands) != 1 {
			t.Fatalf("%s: %+v", tc.command, p)
		}
		x := analyzeBashExecution(f.session, p.Commands[0], f.workspace, vars, environment)
		e := commands.Analyze(types.CommandInput{Name: x.Identity, Args: x.Argv[1:], Words: x.Words[1:], Identity: x.CommandIdentity, Dialect: dialect.Bash})
		found := false
		for _, op := range e.Ops {
			for _, target := range op.Targets {
				if target.Path != tc.path {
					continue
				}
				found = true
				word := target.Word
				if word.Literal != tc.literal {
					t.Fatalf("%s: %+v", tc.command, target)
				}
				if tc.literal && (word.ValueStart < 0 || word.ValueEnd > len(tc.command) || tc.command[word.ValueStart:word.ValueEnd] != tc.raw) {
					t.Fatalf("%s: invalid value span %+v", tc.command, word)
				}
				break
			}
			if found {
				break
			}
		}
		if !found && tc.literal {
			t.Fatalf("%s: target missing: %+v", tc.command, e)
		}
	}
}
