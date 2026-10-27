package llm

import "testing"

func TestToolActivityDisplay(t *testing.T) {
	for input, want := range map[string]string{
		"更新 README 安装说明":                                                         "更新 README 安装说明",
		"https://example.com/pricing/?token=secret#a":                            "example.com/pricing",
		"/Users/me/project/docs/README.md":                                       "README.md",
		"@workspace/docs/README.md":                                              "@workspace/docs/README.md",
		"line one\n  line two":                                                   "line one line two",
		"0123456789012345678901234567890123456789012345678901234567890123456789": "01234567890123456789012345678901234567890123456789012345678…",
	} {
		if got := toolActivityDisplay(input); got != want {
			t.Fatalf("toolActivityDisplay(%q)=%q want %q", input, got, want)
		}
	}
}
