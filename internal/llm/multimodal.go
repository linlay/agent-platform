package llm

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-platform-runner-go/internal/api"
)

const (
	maxInlineImageBytes    = 20 * 1024 * 1024
	reencodeThresholdBytes = 400 * 1024 // 超过此体积才重编码
	reencodeJPEGQuality    = 92         // 接近无损，对手写文字影响可忽略
)

// buildUserMessageContent assembles the user message payload for the LLM.
// When the request has image references in the chat dir, it returns an
// OpenAI-compatible multimodal content array: [{type:text,...},{type:image_url,...},...].
// Otherwise it returns the plain text string so existing callers keep working.
func buildUserMessageContent(chatsDir string, chatID string, text string, references []api.Reference) any {
	imageBlocks := collectImageBlocks(chatsDir, chatID, references)
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

func collectImageBlocks(chatsDir string, chatID string, references []api.Reference) []map[string]any {
	if len(references) == 0 || strings.TrimSpace(chatsDir) == "" || strings.TrimSpace(chatID) == "" {
		return nil
	}

	blocks := make([]map[string]any, 0, len(references))
	for _, ref := range references {
		mime := strings.ToLower(strings.TrimSpace(ref.MimeType))
		if !isSupportedImageMime(mime) {
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
		data, err := readBoundedFile(hostPath, maxInlineImageBytes)
		if err != nil {
			log.Printf("[llm][multimodal] skip image ref name=%q path=%q err=%v", name, hostPath, err)
			continue
		}
		outMime := mime
		// 高质量 JPEG 重编码——对大于阈值的图减小传输体积，但保持接近无损的视觉质量，
		// 确保 VL 模型对手写/细节的识别不受影响。
		if len(data) > reencodeThresholdBytes {
			if shrunk, shrunkMime, ok := shrinkImage(data); ok {
				log.Printf("[llm][multimodal] reencoded image name=%q %d->%d bytes (q=%d)",
					name, len(data), len(shrunk), reencodeJPEGQuality)
				data = shrunk
				outMime = shrunkMime
			}
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		blocks = append(blocks, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:" + outMime + ";base64," + encoded,
			},
		})
	}
	return blocks
}

func isSupportedImageMime(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/jpg", "image/webp", "image/gif":
		return true
	}
	return false
}

// shrinkImage decodes raw image bytes and re-encodes as high-quality JPEG
// (quality 92, near-lossless for handwritten content) to reduce HTTP payload
// size while preserving the detail VL models need. Returns (newBytes,
// "image/jpeg", true) on success; falls back to original on any failure.
func shrinkImage(data []byte) ([]byte, string, bool) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: reencodeJPEGQuality}); err != nil {
		return nil, "", false
	}
	if buf.Len() >= len(data) {
		return nil, "", false
	}
	return buf.Bytes(), "image/jpeg", true
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, os.ErrInvalid
	}
	buf := make([]byte, info.Size())
	if _, err := f.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}
