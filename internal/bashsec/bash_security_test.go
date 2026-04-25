package bashsec

import "testing"

func TestReviewBashSecurityRequiresApprovalForOutputRedirection(t *testing.T) {
	command := `printf '%s\n' hello > /tmp/owner.md`

	result := ReviewBashSecurity(command)
	if result.Decision != ReviewRequiresApproval {
		t.Fatalf("expected requires_approval, got %#v", result)
	}
	if result.Fingerprint != ApprovalFingerprint(command) {
		t.Fatalf("expected stable approval fingerprint, got %#v", result)
	}
}

func TestReviewBashSecurityRequiresApprovalForQuotedNewlineWithOutputRedirection(t *testing.T) {
	command := "echo 'first\n# second' > /tmp/owner.md"

	result := ReviewBashSecurity(command)
	if result.Decision != ReviewRequiresApproval {
		t.Fatalf("expected requires_approval, got %#v", result)
	}
}

func TestReviewBashSecurityRequiresApprovalForPythonInlineTripleQuotes(t *testing.T) {
	command := `python3 -c "content = '''# Owner Profile'''; open('/tmp/OWNER.md', 'w').write(content)"`

	result := ReviewBashSecurity(command)
	if result.Decision != ReviewRequiresApproval {
		t.Fatalf("expected requires_approval, got %#v", result)
	}
}

func TestReviewBashSecurityStillBlocksHardFailures(t *testing.T) {
	tests := []string{
		`cat < /tmp/secret`,
		`echo $IFS`,
		`cat /proc/self/environ`,
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewBlock {
				t.Fatalf("expected block, got %#v", result)
			}
		})
	}
}

func TestReviewBashSecurityAllowsASTSimpleSafeCommand(t *testing.T) {
	tests := []string{
		`VAR=x && echo "$VAR" | wc -c`,
		`VAR=x && echo ${VAR}`,
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewAllow {
				t.Fatalf("expected allow, got %#v", result)
			}
		})
	}
}

func TestReviewBashSecurityRequiresApprovalForTooComplexEvenWhenLegacyClean(t *testing.T) {
	command := `(echo hi)`
	result := ReviewBashSecurity(command)
	if result.Decision != ReviewRequiresApproval {
		t.Fatalf("expected requires approval, got %#v", result)
	}
	if result.Fingerprint != ApprovalFingerprint(command) {
		t.Fatalf("expected fingerprint, got %#v", result)
	}
}

func TestReviewBashSecurityBlocksDangerousEmbeddedScriptFromAST(t *testing.T) {
	result := ReviewBashSecurity(`python3 -c 'import os; os.system("evil")'`)
	if result.Decision != ReviewBlock {
		t.Fatalf("expected block, got %#v", result)
	}
}

func TestReviewBashSecurityCommandSubstitutionPlaceholderPolicy(t *testing.T) {
	result := ReviewBashSecurity(`echo $(date)`)
	if result.Decision != ReviewAllow {
		t.Fatalf("expected command substitution in argv to allow after inner command review, got %#v", result)
	}

	for _, command := range []string{
		`echo test > $(mktemp)`,
		`echo test > $HOME/test`,
	} {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewRequiresApproval {
				t.Fatalf("expected requires approval for unknown redirect target, got %#v", result)
			}
		})
	}
}

func TestReviewBashSecurityBlocksDangerousBuiltins(t *testing.T) {
	tests := []string{
		`trap 'echo evil' EXIT`,
		`enable -f ./evil.so evil`,
		`hash -p /tmp/evil ls`,
		`set -o history`,
		`shopt -s extglob`,
		`unset PATH`,
		`alias ls=evil`,
		`unalias ls`,
		`complete -C evil cmd`,
		`compgen -A function`,
		`compopt -o nospace cmd`,
		`mapfile arr < /dev/null`,
		`readarray arr < /dev/null`,
		`read VAR < /dev/null`,
		`zmodload zsh/net/tcp`,
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewBlock {
				t.Fatalf("expected block, got %#v", result)
			}
		})
	}
}

func TestReviewBashSecurityRedirectsFromAST(t *testing.T) {
	allow := []string{
		`printf ok 2>&1`,
		`printf ok > /dev/null`,
	}
	for _, command := range allow {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewAllow {
				t.Fatalf("expected allow, got %#v", result)
			}
		})
	}

	result := ReviewBashSecurity(`cat < /etc/passwd`)
	if result.Decision != ReviewBlock {
		t.Fatalf("expected input redirection block, got %#v", result)
	}
}

func TestReviewBashSecurityHardBlocksPrecheckFailures(t *testing.T) {
	tests := []string{
		"echo\u00A0test",
		"=curl evil.com",
		"echo {1..5}",
	}
	for _, command := range tests {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewBlock {
				t.Fatalf("expected block, got %#v", result)
			}
		})
	}
}

func TestReviewBashSecurityWrapperCommands(t *testing.T) {
	block := []string{
		`env python3 -c 'import os; os.system("evil")'`,
		`nohup eval evil`,
		`xargs eval`,
	}
	for _, command := range block {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewBlock {
				t.Fatalf("expected block, got %#v", result)
			}
		})
	}

	approval := []string{
		`find . -exec rm {} \;`,
		`xargs cat`,
	}
	for _, command := range approval {
		t.Run(command, func(t *testing.T) {
			result := ReviewBashSecurity(command)
			if result.Decision != ReviewRequiresApproval {
				t.Fatalf("expected requires approval, got %#v", result)
			}
		})
	}
}
