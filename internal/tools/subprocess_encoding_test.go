package tools

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

func TestDecodeSubprocessOutputBytesDecodesGB18030CodePage(t *testing.T) {
	raw := []byte{0xB2, 0xE2, 0xCA, 0xD4, 0xCE, 0xC4, 0xBC, 0xFE, '.', 't', 'x', 't'}

	got := decodeSubprocessOutputBytes(raw, 936)

	if got != "测试文件.txt" {
		t.Fatalf("expected GB18030 decoded filename, got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("decoded output must be valid UTF-8: %q", got)
	}
}

func TestDecodeSubprocessOutputBytesDecodesBig5CodePage(t *testing.T) {
	raw, _, err := transform.Bytes(traditionalchinese.Big5.NewEncoder(), []byte("測試文件.txt"))
	if err != nil {
		t.Fatalf("encode Big5 fixture: %v", err)
	}

	got := decodeSubprocessOutputBytes(raw, 950)

	if got != "測試文件.txt" {
		t.Fatalf("expected Big5 decoded filename, got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("decoded output must be valid UTF-8: %q", got)
	}
}

func TestDecodeSubprocessOutputBytesKeepsUTF8Output(t *testing.T) {
	raw := []byte("测试文件.txt\n")

	got := decodeSubprocessOutputBytes(raw, 936)

	if got != string(raw) {
		t.Fatalf("expected UTF-8 output to be unchanged, got %q", got)
	}
}

func TestDecodeSubprocessOutputBytesFallsBackToValidUTF8(t *testing.T) {
	got := decodeSubprocessOutputBytes([]byte{0xff, 0xfe, 'x'}, 0)

	if !utf8.ValidString(got) {
		t.Fatalf("fallback output must be valid UTF-8: %q", got)
	}
	if _, err := json.Marshal(map[string]string{"output": got}); err != nil {
		t.Fatalf("fallback output must marshal to JSON: %v", err)
	}
}

func TestTruncateStringBytesPreservesUTF8Boundary(t *testing.T) {
	got := truncateStringBytes("ab测试", 5)

	if got != "ab测" {
		t.Fatalf("expected truncation at rune boundary, got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated string must be valid UTF-8: %q", got)
	}
}
