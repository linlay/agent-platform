package tools

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateStringBytesPreservesUTF8Boundary(t *testing.T) {
	got := truncateStringBytes("ab测试", 5)

	if got != "ab测" {
		t.Fatalf("expected truncation at rune boundary, got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated string must be valid UTF-8: %q", got)
	}
}
