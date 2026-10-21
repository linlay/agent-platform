package contracts

import (
	"strings"
	"testing"
)

func TestCompactToolModelOutputSanitizesWithoutMutatingStructuredResult(t *testing.T) {
	content := `prefix {"contentBase64":"QUJDREVGRw=="} suffix`
	structured := map[string]any{
		"contentBase64": "QUJDREVGRw==",
		"content":       content,
		"path":          "image.png",
	}

	output := CompactToolModelOutput(structured, "fallback")
	if strings.Contains(output, "QUJDREVGRw==") {
		t.Fatalf("compact output leaked base64: %s", output)
	}
	for _, want := range []string{
		`"contentBase64Omitted":true`,
		`"contentBase64Chars":12`,
		`"embeddedBase64Omitted":true`,
		`"path":"image.png"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("compact output missing %s: %s", want, output)
		}
	}
	if structured["contentBase64"] != "QUJDREVGRw==" || structured["content"] != content {
		t.Fatalf("structured result was mutated: %#v", structured)
	}
}

func TestCompactToolModelOutputTruncatesContentAtSafetyLimit(t *testing.T) {
	content := strings.Repeat("x", ToolModelOutputMaxBytes+50)
	output := CompactToolModelOutput(map[string]any{"content": content}, "")
	if !strings.Contains(output, `"contentTruncatedForLLM":true`) ||
		!strings.Contains(output, `"contentBytes":40050`) ||
		!strings.Contains(output, "content truncated for LLM") {
		t.Fatalf("expected bounded compact output, got length=%d suffix=%q", len(output), output[len(output)-120:])
	}
	if strings.Contains(output, content) {
		t.Fatal("compact output retained unbounded content")
	}
}

func TestCompactToolModelOutputUsesFallbackWithoutStructuredResult(t *testing.T) {
	if got := CompactToolModelOutput(nil, "standard output"); got != "standard output" {
		t.Fatalf("expected standard output fallback, got %q", got)
	}
}

func TestCompactToolModelOutputCutsOnRuneBoundaryAndKeepsMarkup(t *testing.T) {
	content := "<a>&" + strings.Repeat("中", ToolModelOutputMaxBytes/3+10)
	output := CompactToolModelOutput(map[string]any{"content": content}, "")
	if strings.Contains(output, `\u003c`) || strings.Contains(output, `\u0026`) || !strings.Contains(output, `<a>&`) {
		t.Fatalf("expected markup without HTML escaping, got prefix %q", output[:40])
	}
	if strings.ContainsRune(output, '\uFFFD') {
		t.Fatal("truncation split a multi-byte rune")
	}
}

func TestModelJSONStringLenMatchesEncoder(t *testing.T) {
	for _, value := range []string{"", "plain", "a\"b\\c\n\r\t", "\x01\x1f", "<tag>&", "中文\u2028\u2029", "bad\xff"} {
		data, err := MarshalModelJSON(value)
		if err != nil {
			t.Fatalf("marshal %q: %v", value, err)
		}
		if got, want := ModelJSONStringLen(value), len(data)-2; got != want {
			t.Fatalf("ModelJSONStringLen(%q)=%d, encoder=%d (%s)", value, got, want, data)
		}
	}
}
