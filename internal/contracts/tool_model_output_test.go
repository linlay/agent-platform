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
	content := strings.Repeat("x", ToolModelOutputMaxChars+50)
	output := CompactToolModelOutput(map[string]any{"content": content}, "")
	if !strings.Contains(output, `"contentTruncatedForLLM":true`) ||
		!strings.Contains(output, `"contentChars":20050`) ||
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
