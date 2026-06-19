package llm

import (
	"log"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/filetools"
	"agent-platform/internal/multimodal"
)

// buildUserMessageContent assembles the user message payload for the LLM.
// When the request has image references in the chat dir, it returns an
// OpenAI-compatible multimodal content array: [{type:text,...},{type:image_url,...},...].
// Otherwise it returns the plain text string so existing callers keep working.
func buildUserMessageContent(chatsDir string, chatID string, text string, references []api.Reference, isVision bool, logMedia bool) any {
	if !isVision {
		return text
	}

	imageBlocks := collectImageBlocks(chatsDir, chatID, references, logMedia)
	if len(imageBlocks) == 0 {
		return text
	}

	content := make([]map[string]any, 0, 1+len(imageBlocks))
	if strings.TrimSpace(text) != "" {
		content = append(content, map[string]any{
			"type": "text",
			"text": text,
		})
	}
	content = append(content, imageBlocks...)
	return content
}

func collectImageBlocks(chatsDir string, chatID string, references []api.Reference, logMedia bool) []map[string]any {
	if len(references) == 0 || strings.TrimSpace(chatsDir) == "" || strings.TrimSpace(chatID) == "" {
		return nil
	}

	blocks := make([]map[string]any, 0, len(references))
	for _, ref := range references {
		mime := strings.ToLower(strings.TrimSpace(ref.MimeType))
		if !filetools.IsSupportedImageMime(mime) {
			continue
		}
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			if sb := strings.TrimSpace(ref.SandboxPath); sb != "" {
				name = filepath.Base(sb)
			}
		}
		if name == "" {
			continue
		}
		hostPath := filepath.Join(chatsDir, chatID, name)
		image, err := multimodal.LoadImageFile(hostPath, mime, multimodal.DefaultImageLoadOptions())
		if err != nil {
			log.Printf("[llm][multimodal] skip image ref name=%q path=%q err=%v", name, hostPath, err)
			continue
		}
		if image.Reencoded && logMedia {
			log.Printf("[llm][multimodal] reencoded image name=%q sentBytes=%d", name, image.SentBytes)
		}
		blocks = append(blocks, multimodal.OpenAIImageBlock(image))
	}
	return blocks
}
