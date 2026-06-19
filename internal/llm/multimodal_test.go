package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func TestBuildUserMessageContentIncludesImageReferences(t *testing.T) {
	chatsDir := t.TempDir()
	chatID := "chat_1"
	if err := os.MkdirAll(filepath.Join(chatsDir, chatID), 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chatsDir, chatID, "demo.png"), []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	content := buildUserMessageContent(chatsDir, chatID, "what is in this image?", []api.Reference{{
		Name:     "demo.png",
		MimeType: "image/png",
	}}, true, false)

	blocks, ok := content.([]map[string]any)
	if !ok {
		t.Fatalf("expected multimodal content blocks, got %T", content)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected text + image blocks, got %d", len(blocks))
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "what is in this image?" {
		t.Fatalf("unexpected text block: %#v", blocks[0])
	}
	imageURL, ok := blocks[1]["image_url"].(map[string]any)
	if !ok || blocks[1]["type"] != "image_url" {
		t.Fatalf("unexpected image block: %#v", blocks[1])
	}
	url, _ := imageURL["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("expected data URL image payload, got %q", url)
	}
}

func TestBuildUserMessageContentFallsBackToTextWithoutImages(t *testing.T) {
	content := buildUserMessageContent(t.TempDir(), "chat_1", "hello", []api.Reference{{
		Name:     "notes.txt",
		MimeType: "text/plain",
	}}, true, false)

	if content != "hello" {
		t.Fatalf("expected plain text fallback, got %#v", content)
	}
}

func TestBuildUserMessageContentSkipsImagesWhenNotVision(t *testing.T) {
	chatsDir := t.TempDir()
	chatID := "chat_1"
	if err := os.MkdirAll(filepath.Join(chatsDir, chatID), 0o755); err != nil {
		t.Fatalf("mkdir chat dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chatsDir, chatID, "demo.png"), []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	content := buildUserMessageContent(chatsDir, chatID, "hello", []api.Reference{{
		Name:     "demo.png",
		MimeType: "image/png",
	}}, false, false)

	if content != "hello" {
		t.Fatalf("expected plain text for non-vision model, got %#v", content)
	}
}

func TestAnthropicContentBlocksConvertsImageDataURL(t *testing.T) {
	blocks := anthropicContentBlocks([]map[string]any{
		{
			"type": "text",
			"text": "look",
		},
		{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:image/png;base64,cG5nLWJ5dGVz",
			},
		},
	})

	if len(blocks) != 2 {
		t.Fatalf("expected text + image blocks, got %#v", blocks)
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "look" {
		t.Fatalf("unexpected text block: %#v", blocks[0])
	}
	source, ok := blocks[1]["source"].(map[string]any)
	if !ok || blocks[1]["type"] != "image" {
		t.Fatalf("unexpected image block: %#v", blocks[1])
	}
	if source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "cG5nLWJ5dGVz" {
		t.Fatalf("unexpected image source: %#v", source)
	}
}
