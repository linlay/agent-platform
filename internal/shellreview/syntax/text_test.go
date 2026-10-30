package syntax

import "testing"

func TestTextGuardIsIndependentOfGrammarAndQuoting(t *testing.T) {
	for _, bad := range []string{
		"\x00", "\x1b[2K", "\x7f", "\u0085", "\v", "\f", "\r",
		"\u202a", "\u202b", "\u202c", "\u202d", "\u202e", "\u2066", "\u2067", "\u2068", "\u2069", "\xff",
	} {
		for _, command := range []string{"echo " + bad + "suffix", "echo '" + bad + "'", "Write-Output \"" + bad + "\"", "cat <<EOF\n" + bad + "tail\nEOF"} {
			if reason := CheckText(command); reason == "" || !IsTextRejection(reason) {
				t.Fatalf("unsafe text %q was not a hard rejection: %q", command, reason)
			}
		}
	}
	for _, command := range []string{
		"echo 中文", "echo 'café'", "echo 😀", "echo one\n\techo two", "echo one\r\necho two",
		// Hidden characters are judged by the caller's quoting rules.
		"git commit -m \"👨\u200d💻 修复\"", "echo '\u3000\u3000第一段'", "echo 'می\u200cخواهم'", "echo 'שלום\u200f'",
	} {
		if reason := CheckText(command); reason != "" {
			t.Fatalf("ordinary text %q was rejected: %s", command, reason)
		}
	}
}

func TestHiddenCharactersAreReportedForTheTextGiven(t *testing.T) {
	for _, bad := range []string{
		"\u00a0", "\u1680", "\u2000", "\u2028", "\u2029", "\u3000",
		"\u00ad", "\u061c", "\u200b", "\u200c", "\u200d", "\u200e", "\u200f", "\u2060", "\ufeff", "\U000e0041", "\u034f",
	} {
		reason := CheckHidden("echo" + bad + "x")
		if reason == "" || !IsTextRejection(reason) || CheckAll("'"+bad+"'") == "" {
			t.Fatalf("hidden character %q was not reported: %q", bad, reason)
		}
	}
	for _, text := range []string{"echo ok", "echo\tok\n", "echo 😀 中文 ❤\ufe0f"} {
		if reason := CheckAll(text); reason != "" {
			t.Fatalf("ordinary text %q was rejected: %s", text, reason)
		}
	}
}
