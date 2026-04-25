package bashast

import "testing"

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
