package bash

import (
	"strings"
	"testing"
)

func TestParseForSecurityScopeIsolationAcrossOr(t *testing.T) {
	result := ParseForSecurity(`true || FLAG=--dry-run && cmd $FLAG`)
	if result.Kind != TooComplex {
		t.Fatalf("expected too complex for flag omission pattern, got %#v", result)
	}
}

func TestParseForSecurityScopeIsolationAcrossPipe(t *testing.T) {
	result := ParseForSecurity(`VAR=secret | cmd $VAR`)
	if result.Kind != TooComplex {
		t.Fatalf("expected too complex for pipeline-scoped variable, got %#v", result)
	}
}

func TestParseForSecurityExportAndLoops(t *testing.T) {
	result := ParseForSecurity(`export FOO=bar; for item in a b; do echo "$item"; done`)
	if result.Kind != Simple {
		t.Fatalf("expected simple, got %#v", result)
	}
	if len(result.Commands) != 2 {
		t.Fatalf("expected export and echo commands, got %#v", result.Commands)
	}
	if result.Commands[0].Argv[0] != "export" || result.Commands[0].EnvVars[0].Name != "FOO" {
		t.Fatalf("unexpected export command %#v", result.Commands[0])
	}
	if result.Commands[1].Argv[0] != "echo" || result.Commands[1].Argv[1] != TrackedVariablePlaceholder {
		t.Fatalf("unexpected loop echo command %#v", result.Commands[1])
	}
}

func TestParseForSecurityDeclareSafety(t *testing.T) {
	tooComplex := []string{
		`declare -n ref=PATH`,
		`declare -i x='a[$(id)]'`,
		`declare -a items`,
		`declare 'x[$(id)]=val'`,
	}
	for _, command := range tooComplex {
		t.Run(command, func(t *testing.T) {
			result := ParseForSecurity(command)
			if result.Kind != TooComplex {
				t.Fatalf("expected too complex, got %#v", result)
			}
		})
	}

	simple := []string{
		`declare -r FOO=bar`,
		`export FOO=bar`,
	}
	for _, command := range simple {
		t.Run(command, func(t *testing.T) {
			result := ParseForSecurity(command)
			if result.Kind != Simple {
				t.Fatalf("expected simple, got %#v", result)
			}
		})
	}
}

func TestParseForSecurityBareVarUnsafeCharacters(t *testing.T) {
	result := ParseForSecurity(`VAR="-rf /" && rm $VAR`)
	if result.Kind != TooComplex {
		t.Fatalf("expected unsafe bare variable to be too complex, got %#v", result)
	}

	result = ParseForSecurity(`VAR="-rf /" && rm "$VAR"`)
	if result.Kind != Simple {
		t.Fatalf("expected quoted unsafe variable to remain simple, got %#v", result)
	}
}

func TestParseForSecurityIfAndWhileExtractBodies(t *testing.T) {
	result := ParseForSecurity(`if test -f x; then echo yes; else echo no; fi; while test -f y; do echo loop; done`)
	if result.Kind != Simple {
		t.Fatalf("expected simple, got %#v", result)
	}
	var bases []string
	for _, cmd := range result.Commands {
		if len(cmd.Argv) > 0 {
			bases = append(bases, cmd.Argv[0])
		}
	}
	want := []string{"test", "echo", "echo", "test", "echo"}
	if len(bases) != len(want) {
		t.Fatalf("expected bases %#v, got %#v", want, bases)
	}
	for idx := range want {
		if bases[idx] != want[idx] {
			t.Fatalf("expected bases %#v, got %#v", want, bases)
		}
	}
}

func TestForLoopIsWalkedPerStaticValue(t *testing.T) {
	var asked [][]string
	globs := func(pattern string, dirs []string) ([]string, bool) {
		asked = append(asked, append([]string{pattern}, dirs...))
		return []string{"/m/x", "/m/y"}, true
	}
	argvs := func(command string) []string {
		t.Helper()
		result := ParseForExecution(command, map[string]string{"HOME": "/home/u"}, globs)
		if result.Kind != Simple {
			t.Fatalf("%s: %#v", command, result)
		}
		var out []string
		for _, cmd := range result.Commands {
			out = append(out, strings.Join(cmd.Argv, " "))
		}
		return out
	}
	for command, want := range map[string]string{
		`for f in a "b c" {d,e}.txt; do cat "$f"; done`: "cat a|cat b c|cat d.txt|cat e.txt",
		`for f in *.txt; do cat "$f" "$f.bak"; done`:    "cat /m/x /m/x.bak|cat /m/y /m/y.bak",
		`for f in ~/notes; do cat "$f"; done`:           "cat /home/u/notes",
		`for f in; do cat "$f"; done`:                   "",
		`for f in $(ls); do cat "$f"; done`:             "ls|cat " + TrackedVariablePlaceholder,
		`for f in '*'; do cat "$f"; done`:               "cat " + TrackedVariablePlaceholder,
	} {
		if got := strings.Join(argvs(command), "|"); got != want {
			t.Errorf("%s: got %q, want %q", command, got, want)
		}
	}
	asked = nil
	argvs(`cd /a && cd b; for f in *; do cat "$f"; done`)
	if len(asked) != 1 || strings.Join(asked[0], ",") != "*,,/a,/a/b" {
		t.Errorf("relative glob must be expanded in every possible cwd: %v", asked)
	}
	asked = nil
	argvs(`cd /a && for f in *; do cat "$f"; done; for g in *; do cat "$g"; done`)
	if len(asked) != 2 || strings.Join(asked[0], ",") != "*,/a" || strings.Join(asked[1], ",") != "*,,/a" {
		t.Errorf("after cd && only the cd target is a possible cwd: %v", asked)
	}
	asked = nil
	if got := argvs(`cd "$(pwd)/x"; for f in *; do cat "$f"; done`); len(asked) != 0 || got[2] != "cat "+TrackedVariablePlaceholder {
		t.Errorf("an unknown cd keeps a relative glob unexpanded: %v %v", asked, got)
	}
	if got := argvs(`cd "$(pwd)/x"; for f in /abs/*; do cat "$f"; done`); got[2] != "cat /m/x" {
		t.Errorf("an absolute glob ignores the cwd: %v", got)
	}
	// Without an expander, as in the syntax review, loops stay unresolved.
	if result := ParseForExecution(`for f in a b; do cat "$f"; done`, nil); len(result.Commands) != 1 || result.Commands[0].Argv[1] != TrackedVariablePlaceholder {
		t.Errorf("no expander: %#v", result.Commands)
	}
}
