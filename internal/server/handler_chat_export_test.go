package server

import (
	"strings"
	"testing"

	"agent-platform/internal/stream"
)

func TestRenderChatMarkdownSkipsHiddenQuery(t *testing.T) {
	markdown := renderChatMarkdown("Automation", "agent-a", []stream.EventData{
		{
			Type:      "request.query",
			Timestamp: 100,
			Payload: map[string]any{
				"message": "Secret automation prompt",
				"hidden":  true,
			},
		},
		{
			Type:      "content.snapshot",
			Timestamp: 200,
			Payload: map[string]any{
				"text": "Automation result",
			},
		},
	})

	if strings.Contains(markdown, "Secret automation prompt") || strings.Contains(markdown, "## User") {
		t.Fatalf("expected hidden query to be omitted, got:\n%s", markdown)
	}
	if !strings.Contains(markdown, "Automation result") {
		t.Fatalf("expected assistant content to remain, got:\n%s", markdown)
	}
}
