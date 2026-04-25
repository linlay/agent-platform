package bashast

import "testing"

func TestParseForSecuritySimpleCommands(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    [][]string
	}{
		{name: "ls", command: "ls -la", want: [][]string{{"ls", "-la"}}},
		{name: "git", command: "git status", want: [][]string{{"git", "status"}}},
		{name: "echo", command: "echo hello", want: [][]string{{"echo", "hello"}}},
		{name: "pipeline", command: "cat file | grep pattern | wc -l", want: [][]string{{"cat", "file"}, {"grep", "pattern"}, {"wc", "-l"}}},
		{name: "logic", command: "cmd1 && cmd2 || cmd3", want: [][]string{{"cmd1"}, {"cmd2"}, {"cmd3"}}},
		{name: "quotes", command: `echo 'hello world' "again"`, want: [][]string{{"echo", "hello world", "again"}}},
		{name: "quoted json braces", command: `mock create --payload '{"days":3,"team":"engineering"}'`, want: [][]string{{"mock", "create", "--payload", `{"days":3,"team":"engineering"}`}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ParseForSecurity(tc.command)
			if result.Kind != Simple {
				t.Fatalf("expected simple, got %#v", result)
			}
			if len(result.Commands) != len(tc.want) {
				t.Fatalf("expected %d commands, got %#v", len(tc.want), result.Commands)
			}
			for idx, wantArgv := range tc.want {
				got := result.Commands[idx].Argv
				if len(got) != len(wantArgv) {
					t.Fatalf("command %d expected argv %#v, got %#v", idx, wantArgv, got)
				}
				for argIdx := range wantArgv {
					if got[argIdx] != wantArgv[argIdx] {
						t.Fatalf("command %d expected argv %#v, got %#v", idx, wantArgv, got)
					}
				}
			}
		})
	}
}

func TestParseForSecurityVariablesAndRedirects(t *testing.T) {
	result := ParseForSecurity(`VAR=x && echo "$VAR" > out.txt`)
	if result.Kind != Simple {
		t.Fatalf("expected simple, got %#v", result)
	}
	if len(result.Commands) != 1 {
		t.Fatalf("expected one command, got %#v", result.Commands)
	}
	cmd := result.Commands[0]
	if len(cmd.Argv) != 2 || cmd.Argv[0] != "echo" || cmd.Argv[1] != "x" {
		t.Fatalf("unexpected argv %#v", cmd.Argv)
	}
	if len(cmd.Redirects) != 1 || cmd.Redirects[0].Op != ">" || cmd.Redirects[0].Target != "out.txt" {
		t.Fatalf("unexpected redirects %#v", cmd.Redirects)
	}
}

func TestParseForSecurityCommandSubstitutionExtractsInnerCommand(t *testing.T) {
	result := ParseForSecurity(`echo $(date)`)
	if result.Kind != Simple {
		t.Fatalf("expected simple, got %#v", result)
	}
	if len(result.Commands) != 2 {
		t.Fatalf("expected inner and outer commands, got %#v", result.Commands)
	}
	if result.Commands[0].Argv[0] != "date" {
		t.Fatalf("expected date command first, got %#v", result.Commands)
	}
	if result.Commands[1].Argv[1] != CommandSubstitutionPlaceholder {
		t.Fatalf("expected command substitution placeholder, got %#v", result.Commands[1].Argv)
	}
}

func TestParseForSecurityTooComplex(t *testing.T) {
	tests := []string{
		`(echo hi)`,
		`foo() { echo hi; }`,
		`case "$x" in a) echo a;; esac`,
		`cat <(echo hi)`,
		`echo $((1 + 2))`,
		`echo {a,b}`,
		`echo \ hello`,
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			result := ParseForSecurity(command)
			if result.Kind != TooComplex {
				t.Fatalf("expected too complex, got %#v", result)
			}
		})
	}
}
