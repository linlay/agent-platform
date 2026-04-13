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
