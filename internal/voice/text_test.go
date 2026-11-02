package voice

import (
	"strings"
	"testing"
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
	if got != strings.Repeat("长", 2000)+"其余内容请查看完整聊天。" {
		t.Fatal("long text changed beyond the explicit prefix limit")
	}
}
