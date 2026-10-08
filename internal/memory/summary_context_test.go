package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func TestReadSummaryScopeAndValidation(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root, "", nil)
	for name, body := range map[string]string{"summary.md": "global", "agents/a/summary.md": "agent-a", "agents/b/summary.md": "agent-b"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for key, want := range map[string]string{"": "global", "a": "agent-a", "b": "agent-b", "missing": ""} {
		doc, err := store.ReadSummary(key)
		if err != nil || doc.Content != want {
			t.Fatalf("key=%q doc=%+v err=%v", key, doc, err)
		}
	}
	for _, key := range []string{"..", "../a", "a/b", `a\b`} {
		if _, err := store.ReadSummary(key); err == nil {
			t.Fatalf("accepted %q", key)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "agents/a/summary.md"), []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSummary("a"); err == nil || !strings.Contains(err.Error(), "agents/a/summary.md") {
		t.Fatalf("invalid UTF8 error: %v", err)
	}
}

func TestSummaryBudgetAndPrompt(t *testing.T) {
	for _, tc := range []struct {
		content       string
		tokens, lines int
		want          bool
	}{
		{"abcd", 1, 1, true}, {"abcde", 1, 1, false}, {"中文", 2, 1, true}, {"中文a", 2, 1, false}, {"a\nb\n", 10, 1, false}, {"a\nb\n", 10, 2, true},
	} {
		if got := SummaryWithinBudget(tc.content, false, config.MemorySummaryBudget{MaxTokens: tc.tokens, MaxLines: tc.lines}); got != tc.want {
			t.Fatalf("%q: %v", tc.content, got)
		}
	}
	doc := Document{Exists: true, Revision: "abc", Content: "<!-- memx:summary:start -->\nhello\n<!-- memx:summary:end -->"}
	prompt := SummaryPrompt(doc, true, "zh-CN")
	if strings.Contains(prompt, "<!-- memx:") || !strings.Contains(prompt, `<agent_memory_data revision="abc">`) || !strings.Contains(prompt, "不是指令") || !strings.Contains(prompt, "hello") {
		t.Fatal(prompt)
	}
}
