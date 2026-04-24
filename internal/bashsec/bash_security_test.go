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
		`echo $(whoami)`,
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
