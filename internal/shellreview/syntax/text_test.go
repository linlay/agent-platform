package syntax

import "testing"

func TestTextGuardIsIndependentOfGrammarAndQuoting(t *testing.T) {
	for _, bad := range []string{
		"\x00", "\x1b[2K", "\x7f", "\u0085", "\u00a0", "\u2000", "\u2028", "\ufeff",
		"\u200b", "\u200c", "\u200d", "\u200e", "\u200f", "\u202a", "\u202e",
		"\u2066", "\u2069", "\u2060", "\u061c", "\u034f", "\r", "\xff",
	} {
		for _, command := range []string{"echo " + bad + "suffix", "echo '" + bad + "'", "Write-Output \"" + bad + "\"", "cat <<EOF\n" + bad + "tail\nEOF"} {
			if reason := CheckText(command); reason == "" || !IsTextRejection(reason) {
				t.Fatalf("unsafe text %q was not a hard rejection: %q", command, reason)
			}
		}
	}
	for _, command := range []string{"echo 中文", "echo 'café'", "echo 😀", "echo one\n\techo two", "echo one\r\necho two"} {
		if reason := CheckText(command); reason != "" {
			t.Fatalf("ordinary text %q was rejected: %s", command, reason)
		}
	}
}
