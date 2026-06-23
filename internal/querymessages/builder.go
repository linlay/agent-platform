package querymessages

import (
	"log"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/filetools"
	media "agent-platform/internal/multimodal"
	"agent-platform/internal/referenceprompt"
)

// BuildMessages returns the current query-derived messages prepared for the
// model. query.message remains the raw API/user input; these messages are the
// provider-safe model-side representation.
func BuildMessages(chatsDir string, chatID string, role string, text string, references []api.Reference, isVision bool, logMedia bool) []map[string]any {
	providerRole, providerText := api.ProviderSafeQueryMessage(role, text)
	content := BuildContent(chatsDir, chatID, providerText, references, isVision, logMedia)
	return []map[string]any{{
		"role":    providerRole,
		"content": content,
	}}
}

// BuildContent assembles the content payload for a model-side user/query
// message. Vision models receive OpenAI-compatible multimodal blocks when
// image references can be loaded from the chat directory.
func BuildContent(chatsDir string, chatID string, text string, references []api.Reference, isVision bool, logMedia bool) any {
	messageText := referenceprompt.FormatUserMessage(text, references)
	if !isVision {
		return messageText
	}

	imageBlocks := collectImageBlocks(chatsDir, chatID, references, logMedia)
	if len(imageBlocks) == 0 {
		return messageText
	}

	content := make([]map[string]any, 0, 1+len(imageBlocks))
	if strings.TrimSpace(messageText) != "" {
		content = append(content, map[string]any{
			"type": "text",
			"text": messageText,
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
		image, err := media.LoadImageFile(hostPath, mime, media.DefaultImageLoadOptions())
		if err != nil {
			log.Printf("[llm][multimodal] skip image ref name=%q path=%q err=%v", name, hostPath, err)
			continue
		}
		if image.Reencoded && logMedia {
			log.Printf("[llm][multimodal] reencoded image name=%q sentBytes=%d", name, image.SentBytes)
		}
		blocks = append(blocks, media.OpenAIImageBlock(image))
	}
	return blocks
}
