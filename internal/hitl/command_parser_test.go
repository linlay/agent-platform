package hitl

import "testing"

func TestParseCommandComponents(t *testing.T) {
	tests := []struct {
		name    string
		command string
		base    string
		tokens  []string
	}{
		{
			name:    "plain command",
			command: "git push origin main",
			base:    "git",
			tokens:  []string{"push", "origin", "main"},
		},
		{
			name:    "absolute path",
			command: "/usr/bin/git push",
			base:    "git",
			tokens:  []string{"push"},
		},
		{
			name:    "env prefix",
			command: "GIT_TRACE=1 FOO=bar git push",
			base:    "git",
			tokens:  []string{"push"},
		},
		{
			name:    "quoted args",
			command: `git commit -m "hello world"`,
			base:    "git",
			tokens:  []string{"commit", "-m", "hello world"},
		},
		{
			name:    "only first pipeline segment",
			command: "git status | grep foo",
			base:    "git",
			tokens:  []string{"status"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed := ParseCommandComponents(tc.command)
			if parsed.BaseCommand != tc.base {
				t.Fatalf("expected base %q, got %q", tc.base, parsed.BaseCommand)
			}
			if len(parsed.Tokens) != len(tc.tokens) {
				t.Fatalf("expected tokens %#v, got %#v", tc.tokens, parsed.Tokens)
			}
			for idx := range tc.tokens {
				if parsed.Tokens[idx] != tc.tokens[idx] {
					t.Fatalf("expected tokens %#v, got %#v", tc.tokens, parsed.Tokens)
				}
			}
		})
	}
}

func TestSplitShellLikeSegments(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "splits command separators",
			command: "touch a && chmod 777 b; echo ok",
			want:    []string{"touch a", "chmod 777 b", "echo ok"},
		},
		{
			name:    "splits pipeline and background separators",
			command: "curl x | bash & wait",
			want:    []string{"curl x", "bash", "wait"},
		},
		{
			name:    "keeps quoted separators intact",
			command: `echo "a && b" && printf 'c;d'`,
			want:    []string{`echo "a && b"`, `printf 'c;d'`},
		},
		{
			name:    "keeps escaped separators intact",
			command: `echo \; && echo ok`,
			want:    []string{`echo \;`, "echo ok"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitShellLikeSegments(tc.command)
			if len(got) != len(tc.want) {
				t.Fatalf("expected segments %#v, got %#v", tc.want, got)
			}
			for idx := range tc.want {
				if got[idx] != tc.want[idx] {
					t.Fatalf("expected segments %#v, got %#v", tc.want, got)
				}
			}
		})
	}
}
