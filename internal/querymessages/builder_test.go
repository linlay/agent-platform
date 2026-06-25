package querymessages

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
)

func TestBuildContentAdvancedUserPrompt(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	size := int64(537)
	content := BuildContentWithOptions("", "chat-1", "first line\nsecond line", []api.Reference{{
		ID:          "r01",
		Type:        "file",
		Name:        "sales.csv",
		SandboxPath: "/workspace/sales.csv",
		MimeType:    "text/csv",
		SizeBytes:   &size,
	}}, false, false, BuildOptions{
		AdvancedUserPrompt: true,
		RunID:              "run_xxx",
		RequestID:          "req_xxx",
		AgentKey:           "assistant",
		TeamID:             "team_xxx",
		Role:               "user",
		Scene:              &api.Scene{Title: "Sales\nDashboard", URL: "https://example.com/app"},
		Now:                time.Date(2026, 6, 24, 15, 4, 5, 0, location),
	})

	text, ok := content.(string)
	if !ok {
		t.Fatalf("expected text content, got %T", content)
	}
	for _, expected := range []string{
		`<advanced_user_prompt schema="zenmind.user_prompt.v1">`,
		"<run_context>",
		"runId: run_xxx",
		"requestId: req_xxx",
		"agentKey: assistant",
		"teamId: team_xxx",
		"role: user",
		"currentDateTime: 2026-06-24T15:04:05+08:00",
		"timezone: Asia/Shanghai",
		"sceneTitle: Sales\\nDashboard",
		"sceneUrl: https://example.com/app",
		"</run_context>",
		"<references>",
		"- id: r01",
		"  type: file",
		"  name: sales.csv",
		"  sandboxPath: /workspace/sales.csv",
		"  mimeType: text/csv",
		"  sizeBytes: 537",
		"</references>",
		"<user_message>\nfirst line\nsecond line\n</user_message>",
		"</advanced_user_prompt>",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in advanced content, got %q", expected, text)
		}
	}
	for _, unexpected := range []string{"chatId:", "[References]", "[User message]"} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("did not expect %q in advanced content, got %q", unexpected, text)
		}
	}
}

func TestBuildContentAdvancedUserPromptOmitsEmptyOptionalFields(t *testing.T) {
	now := time.Date(2026, 6, 24, 15, 4, 5, 0, time.FixedZone("CST", 8*60*60))
	content := BuildContentWithOptions("", "chat-1", "hello", nil, false, false, BuildOptions{
		AdvancedUserPrompt: true,
		RunID:              "run_xxx",
		AgentKey:           "assistant",
		Role:               "user",
		Now:                now,
		Timezone:           "Asia/Shanghai",
	})

	text, ok := content.(string)
	if !ok {
		t.Fatalf("expected text content, got %T", content)
	}
	for _, unexpected := range []string{"requestId:", "teamId:", "sceneTitle:", "sceneUrl:", "<references>", "chatId:"} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("did not expect %q in advanced content, got %q", unexpected, text)
		}
	}
	if !strings.Contains(text, "<user_message>\nhello\n</user_message>") {
		t.Fatalf("expected user message block, got %q", text)
	}
}

func TestBuildContentDefaultReferenceFormatUnchanged(t *testing.T) {
	content := BuildContentWithOptions("", "chat-1", "hello", []api.Reference{{
		ID:   "r01",
		Name: "sales.csv",
	}}, false, false, BuildOptions{})

	text, ok := content.(string)
	if !ok {
		t.Fatalf("expected text content, got %T", content)
	}
	for _, expected := range []string{
		"[References]",
		"- id: r01",
		"  name: sales.csv",
		"[User message]\nhello",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in default content, got %q", expected, text)
		}
	}
}

func TestBuildContentAdvancedVisionKeepsImageBlocks(t *testing.T) {
	chatsDir := t.TempDir()
	chatID := "chat-1"
	if err := os.MkdirAll(filepath.Join(chatsDir, chatID), 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chatsDir, chatID, "image.png"), []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	content := BuildContentWithOptions(chatsDir, chatID, "hello", []api.Reference{{
		ID:       "r01",
		Name:     "image.png",
		MimeType: "image/png",
	}}, true, false, BuildOptions{
		AdvancedUserPrompt: true,
		RunID:              "run_xxx",
		AgentKey:           "assistant",
		Now:                time.Date(2026, 6, 24, 15, 4, 5, 0, time.UTC),
	})

	blocks, ok := content.([]map[string]any)
	if !ok {
		t.Fatalf("expected multimodal blocks, got %T", content)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected text + image blocks, got %#v", blocks)
	}
	text, _ := blocks[0]["text"].(string)
	if !strings.Contains(text, "<references>") || !strings.Contains(text, "<user_message>\nhello\n</user_message>") {
		t.Fatalf("expected advanced text block with references, got %q", text)
	}
	if blocks[1]["type"] != "image_url" {
		t.Fatalf("expected image block, got %#v", blocks[1])
	}
}
