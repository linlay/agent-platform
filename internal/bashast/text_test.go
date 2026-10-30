package bashast

import "testing"

func TestRawTextQuotingFollowsNestedShellSyntax(t *testing.T) {
	for _, command := range []string{
		"echo \"$(echo\u3000ok)\"",
		"echo \"$(echo ok\u200b)\"",
		"echo \"$(echo \"$(echo\u3000ok)\")\"",
		"cat <<EOF\n'\u3000'\nEOF",
		"echo ok # '\u200b'",
		"echo 'unterminated\u3000",
	} {
		if reason := CheckRawText(command); reason == "" || !IsHardBlockReason(reason) {
			t.Errorf("unquoted hidden text passed: %q (%s)", command, reason)
		}
	}
	for _, command := range []string{
		"echo '\u3000👨\u200d💻'",
		"echo \"$(printf '%s' '\u3000👨\u200d💻')\"",
		"echo \"$(echo \"$(printf '%s' '\u3000')\")\"",
		"echo \"${value:-👨\u200d💻}\"",
	} {
		if reason := CheckRawText(command); reason != "" {
			t.Errorf("quoted data rejected: %q (%s)", command, reason)
		}
	}
}
