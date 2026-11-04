package voice

import "agent-platform/internal/speech"

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSpeechTextOnlyRemovesPresentation(t *testing.T) {
	input := "# 回答\n\n**保留原话** [链接文字](https://secret.example/path) `短代码`\n\n```view key=builtin/demo\n{\"private\":true}\n```\n\n```go\nsecret()\n```\n\n| a | b |\n|---|---|\n| secret | cells |\n"
	got := speechText(input)
	if got != "回答 保留原话 链接文字 短代码" {
		t.Fatalf("%q", got)
	}
	if got := speechText("```view\n{}\n```"); got != "回答包含需要在界面查看的内容。" {
		t.Fatal(got)
	}
	got = speechText(strings.Repeat("长", 2001))
	if got != strings.Repeat("长", maxSpokenRunes)+spokenOverflow {
		t.Fatal("long text changed beyond the explicit prefix limit")
	}
	if n := utf8.RuneCountInString(got); n > speech.MaxSynthesisRunes {
		t.Fatalf("spoken text %d runes exceeds synthesis input limit", n)
	}
}
