package textcodec

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"agent-platform/internal/runtimeenv"

	textencoding "golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func TestLookupEncodingNormalizesGBKAliases(t *testing.T) {
	for _, name := range []string{"gbk", "GB2312", "cp936", "936", "windows-936"} {
		encoding, ok := LookupEncoding(name)
		if !ok || encoding.Label != "gb18030" {
			t.Fatalf("expected %q to resolve to gb18030, got %#v ok=%v", name, encoding, ok)
		}
	}
}

func TestDecodeFileTextKeepsUTF8(t *testing.T) {
	decoded, ok, err := DecodeFileText([]byte("标题=测试\n"), "", runtimeenv.Info{GOOS: "darwin"})
	if err != nil || !ok {
		t.Fatalf("DecodeFileText failed ok=%v err=%v", ok, err)
	}
	if decoded.Encoding != "utf-8" || decoded.Content != "标题=测试\n" {
		t.Fatalf("unexpected decoded text: %#v", decoded)
	}
}

func TestDecodeFileTextDefaultsToGB18030(t *testing.T) {
	raw := encodeTextFixture(t, simplifiedchinese.GB18030, "标题=测试\n")

	decoded, ok, err := DecodeFileText(raw, "", runtimeenv.Info{GOOS: "darwin"})
	if err != nil || !ok {
		t.Fatalf("DecodeFileText failed ok=%v err=%v", ok, err)
	}
	if decoded.Encoding != "gb18030" || decoded.Content != "标题=测试\n" {
		t.Fatalf("unexpected decoded text: %#v", decoded)
	}
}

func TestEncodeFileTextPreservesGB18030(t *testing.T) {
	raw, encoding, err := EncodeFileText("标题=测试\n", "gbk")
	if err != nil {
		t.Fatalf("EncodeFileText: %v", err)
	}
	if encoding != "gb18030" {
		t.Fatalf("expected gb18030 label, got %q", encoding)
	}
	if utf8.Valid(raw) {
		t.Fatalf("expected non-UTF-8 bytes, got %q", string(raw))
	}
	if got := decodeTextFixture(t, simplifiedchinese.GB18030, raw); got != "标题=测试\n" {
		t.Fatalf("unexpected decoded content: %q", got)
	}
}

func TestDefaultFileEncodingCandidatesAreConservative(t *testing.T) {
	candidates := defaultFileEncodingCandidates(runtimeenv.Info{GOOS: "darwin"})
	if len(candidates) != 1 || candidates[0].Label != "gb18030" {
		t.Fatalf("expected only gb18030 on non-Windows, got %#v", candidates)
	}

	candidates = defaultFileEncodingCandidates(runtimeenv.Info{GOOS: "windows", ACP: 950})
	if len(candidates) != 2 || candidates[0].Label != "big5" || candidates[1].Label != "gb18030" {
		t.Fatalf("expected current Windows ACP followed by gb18030, got %#v", candidates)
	}
}

func TestDecodeFileTextRejectsExplicitNonFileEncodings(t *testing.T) {
	for _, encoding := range []string{"big5", "shift_jis", "euc-kr", "cp437", "windows-1252"} {
		t.Run(encoding, func(t *testing.T) {
			if _, ok, err := DecodeFileText([]byte("plain text\n"), encoding, runtimeenv.Info{GOOS: "darwin"}); err == nil || ok {
				t.Fatalf("expected %q to be rejected, ok=%v err=%v", encoding, ok, err)
			}
			if _, _, err := EncodeFileText("plain text\n", encoding); err == nil {
				t.Fatalf("expected EncodeFileText to reject %q", encoding)
			}
		})
	}
}

func TestDecodeSubprocessOutputNonWindowsDoesNotGuessNonUTF8Codepages(t *testing.T) {
	raw := []byte{0xB2, 0xE2, 0xCA, 0xD4, '.', 't', 'x', 't'}

	got := DecodeSubprocessOutput(raw, runtimeenv.Info{GOOS: "darwin"})

	if got == "测试.txt" {
		t.Fatalf("non-Windows subprocess output should not guess GB18030")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("fallback output must be valid UTF-8: %q", got)
	}
}

func TestDecodeSubprocessOutputWindowsACP936(t *testing.T) {
	raw := []byte{0xB2, 0xE2, 0xCA, 0xD4, 0xCE, 0xC4, 0xBC, 0xFE, '.', 't', 'x', 't'}

	got := DecodeSubprocessOutput(raw, runtimeenv.Info{GOOS: "windows", ACP: 936})

	if got != "测试文件.txt" {
		t.Fatalf("expected GB18030 decoded filename, got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("decoded output must be valid UTF-8: %q", got)
	}
}

func TestDecodeSubprocessOutputKeepsUTF8(t *testing.T) {
	raw := []byte("测试文件.txt\n")

	got := DecodeSubprocessOutput(raw, runtimeenv.Info{GOOS: "windows", ACP: 936})

	if got != string(raw) {
		t.Fatalf("expected UTF-8 output to be unchanged, got %q", got)
	}
}

func TestDecodeSubprocessOutputFallbackValidUTF8(t *testing.T) {
	got := DecodeSubprocessOutput([]byte{0xff, 0xfe, 'x'}, runtimeenv.Info{GOOS: "linux"})

	if !utf8.ValidString(got) {
		t.Fatalf("fallback output must be valid UTF-8: %q", got)
	}
	if _, err := json.Marshal(map[string]string{"output": got}); err != nil {
		t.Fatalf("fallback output must marshal to JSON: %v", err)
	}
}

func encodeTextFixture(t *testing.T, codec textencoding.Encoding, content string) []byte {
	t.Helper()
	data, _, err := transform.Bytes(codec.NewEncoder(), []byte(content))
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return data
}

func decodeTextFixture(t *testing.T, codec textencoding.Encoding, data []byte) string {
	t.Helper()
	decoded, _, err := transform.Bytes(codec.NewDecoder(), data)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return string(decoded)
}
