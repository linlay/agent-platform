package bashast

import (
	"strings"
	"testing"
	"time"
)

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

func TestParseForSecurityHeredocRedirect(t *testing.T) {
	command := "cat <<EOF\nhello\nEOF"
	result := ParseForSecurity(command)
	if result.Kind != Simple {
		t.Fatalf("expected simple, got %#v", result)
	}
	if len(result.Commands) != 1 {
		t.Fatalf("expected one command, got %#v", result.Commands)
	}
	cmd := result.Commands[0]
	if len(cmd.Redirects) != 1 {
		t.Fatalf("expected one redirect, got %#v", cmd.Redirects)
	}
	redirect := cmd.Redirects[0]
	if redirect.Op != "<<" || redirect.Target != "EOF" || !redirect.IsHeredoc {
		t.Fatalf("unexpected heredoc redirect %#v", redirect)
	}
	if redirect.HeredocBodyStart < 0 || redirect.HeredocBodyEnd < redirect.HeredocBodyStart || redirect.HeredocBodyEnd > len(command) {
		t.Fatalf("unexpected heredoc body range %#v for command len %d", redirect, len(command))
	}
	if got := command[redirect.HeredocBodyStart:redirect.HeredocBodyEnd]; got != "hello\n" {
		t.Fatalf("expected heredoc body range to cover body, got %q", got)
	}
}

func TestParseForSecurityHeredocCommandSubstitution(t *testing.T) {
	result := ParseForSecurity("cat <<EOF\n$(date)\nEOF")
	if result.Kind != Simple {
		t.Fatalf("expected simple, got %#v", result)
	}
	if len(result.Commands) != 2 {
		t.Fatalf("expected command substitution and parent command, got %#v", result.Commands)
	}
	if len(result.Commands[0].Argv) == 0 || result.Commands[0].Argv[0] != "date" {
		t.Fatalf("expected heredoc command substitution to expose date command, got %#v", result.Commands)
	}
	parent := result.Commands[1]
	if len(parent.Argv) == 0 || parent.Argv[0] != "cat" || len(parent.Redirects) != 1 || !parent.Redirects[0].IsHeredoc {
		t.Fatalf("expected parent cat command with heredoc redirect, got %#v", parent)
	}
}

func TestParseForSecurityExitStatusSpecialParameter(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    [][]string
	}{
		{
			name:    "echo exit status",
			command: `echo "Exit code: $?"`,
			want:    [][]string{{"echo", "Exit code: 0"}},
		},
		{
			name:    "after prior command",
			command: `false; echo "Exit code: $?"`,
			want:    [][]string{{"false"}, {"echo", "Exit code: 0"}},
		},
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

func TestParseForSecurityKnownVariables(t *testing.T) {
	result := ParseForSecurityWithKnownVariables(`echo "$TEST_HOST_ENV"`, map[string]string{"TEST_HOST_ENV": "agent-value"})
	if result.Kind != Simple {
		t.Fatalf("expected known quoted variable to be simple, got %#v", result)
	}
	if len(result.Commands) != 1 || len(result.Commands[0].Argv) != 2 || result.Commands[0].Argv[1] != "agent-value" {
		t.Fatalf("unexpected argv for known variable, got %#v", result.Commands)
	}

	result = ParseForSecurityWithKnownVariables(`rm $TEST_HOST_ENV`, map[string]string{"TEST_HOST_ENV": "-rf /"})
	if result.Kind != TooComplex {
		t.Fatalf("expected unsafe bare known variable to be too complex, got %#v", result)
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
		`echo $'evil'`,
		"echo `date`",
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

func TestParseForSecurityPrechecks(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{name: "control character", command: "echo\x00evil"},
		{name: "unicode whitespace", command: "echo\u00A0test"},
		{name: "zsh tilde bracket", command: "ls ~[test]"},
		{name: "zsh equals expansion", command: "=curl evil.com"},
		{name: "brace expansion", command: "echo {1..5}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := ParseForSecurity(tc.command)
			if result.Kind != TooComplex {
				t.Fatalf("expected too complex, got %#v", result)
			}
			if !IsHardBlockReason(result.Reason) {
				t.Fatalf("expected hard block precheck reason, got %q", result.Reason)
			}
		})
	}

	result := ParseForSecurity(`echo '{a,b}'`)
	if result.Kind != Simple {
		t.Fatalf("expected quoted brace string to remain simple, got %#v", result)
	}
}

func TestParseForSecurityDeepInputReturnsTooComplexPromptly(t *testing.T) {
	command := "echo " + strings.Repeat("$(", 2000) + "date" + strings.Repeat(")", 2000)
	started := time.Now()
	result := ParseForSecurity(command)
	if result.Kind != TooComplex {
		t.Fatalf("expected too complex, got %#v", result)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("expected parser protection to return promptly, took %s", elapsed)
	}
}
