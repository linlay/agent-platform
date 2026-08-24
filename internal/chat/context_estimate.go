package chat

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"
)

const (
	compactImageHeaderReadLimit    = 256 * 1024
	compactImagePixelsPerToken     = 750
	compactImageMinTokens          = 256
	compactImageFallbackTokens     = 8192
	compactImageMaxTokens          = 32768
	compactExternalURLPreviewRunes = 512
)

// projectCompactMediaMessages replaces opaque image bytes with bounded,
// auditable metadata. It never mutates the source messages. mediaTokens is the
// visual-token estimate that must be added when the projected JSON is used to
// estimate a provider request; a text-only compact prompt uses only projected.
func projectCompactMediaMessages(messages []map[string]any, includeDigest bool) (projected []map[string]any, mediaTokens int) {
	if len(messages) == 0 {
		return nil, 0
	}
	projected = make([]map[string]any, len(messages))
	for index, message := range messages {
		cloned := cloneCompactMap(message)
		if content, exists := message["content"]; exists {
			cloned["content"], mediaTokens = projectCompactMediaContent(content, includeDigest, mediaTokens)
		}
		projected[index] = cloned
	}
	return projected, mediaTokens
}

func projectCompactMediaContent(content any, includeDigest bool, mediaTokens int) (any, int) {
	switch typed := content.(type) {
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			block, ok := item.(map[string]any)
			if !ok {
				out[index] = item
				continue
			}
			projected, tokens, found := projectCompactMediaBlock(block, includeDigest)
			if found {
				out[index] = projected
				mediaTokens += tokens
			} else {
				out[index] = block
			}
		}
		return out, mediaTokens
	case []map[string]any:
		out := make([]map[string]any, len(typed))
		for index, block := range typed {
			projected, tokens, found := projectCompactMediaBlock(block, includeDigest)
			if found {
				out[index] = projected
				mediaTokens += tokens
			} else {
				out[index] = block
			}
		}
		return out, mediaTokens
	default:
		return content, mediaTokens
	}
}

func projectCompactMediaBlock(block map[string]any, includeDigest bool) (map[string]any, int, bool) {
	typeName := strings.ToLower(strings.TrimSpace(stringFromAny(block["type"])))
	if typeName == "image_url" {
		imageURL := ""
		switch typed := block["image_url"].(type) {
		case map[string]any:
			imageURL = strings.TrimSpace(stringFromAny(typed["url"]))
		case string:
			imageURL = strings.TrimSpace(typed)
		}
		return compactImageReferenceBlock(imageURL, includeDigest)
	}
	if typeName == "image" {
		source, _ := block["source"].(map[string]any)
		if strings.EqualFold(strings.TrimSpace(stringFromAny(source["type"])), "base64") {
			mimeType := strings.TrimSpace(stringFromAny(source["media_type"]))
			payload := strings.TrimSpace(stringFromAny(source["data"]))
			return compactInlineImageReferenceBlock(mimeType, payload, includeDigest)
		}
	}
	return nil, 0, false
}

func compactImageReferenceBlock(rawURL string, includeDigest bool) (map[string]any, int, bool) {
	mimeType, payload, ok := parseCompactDataImageURL(rawURL)
	if ok {
		return compactInlineImageReferenceBlock(mimeType, payload, includeDigest)
	}
	metadata := map[string]any{"source": "external_url"}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(rawURL)), "data:") {
		metadata["source"] = "invalid_inline_data"
		metadata["encodedChars"] = len(rawURL)
	} else if rawURL != "" {
		metadata["url"] = truncateCompactMediaURL(rawURL, compactExternalURLPreviewRunes)
	}
	if includeDigest && rawURL != "" {
		metadata["payloadSha256"] = compactMediaSHA256(rawURL)
	}
	return map[string]any{"type": "image_reference", "image_reference": metadata}, compactImageFallbackTokens, true
}

func compactInlineImageReferenceBlock(mimeType, payload string, includeDigest bool) (map[string]any, int, bool) {
	width, height, dimensionsOK := compactImageDimensions(payload)
	tokens := compactImageFallbackTokens
	if dimensionsOK {
		tokens = compactImageTokenEstimate(width, height)
	}
	metadata := map[string]any{
		"source":    "inline_data",
		"mimeType":  strings.ToLower(strings.TrimSpace(mimeType)),
		"sizeBytes": compactBase64DecodedSize(payload),
	}
	if dimensionsOK {
		metadata["width"] = width
		metadata["height"] = height
	}
	if includeDigest {
		metadata["payloadSha256"] = compactMediaSHA256(payload)
	}
	return map[string]any{"type": "image_reference", "image_reference": metadata}, tokens, true
}

func parseCompactDataImageURL(value string) (mimeType, payload string, ok bool) {
	value = strings.TrimSpace(value)
	comma := strings.IndexByte(value, ',')
	if comma <= len("data:image/") {
		return "", "", false
	}
	header := value[:comma]
	lowerHeader := strings.ToLower(header)
	if !strings.HasPrefix(lowerHeader, "data:image/") || !strings.HasSuffix(lowerHeader, ";base64") {
		return "", "", false
	}
	semicolon := strings.IndexByte(header, ';')
	if semicolon <= len("data:") {
		return "", "", false
	}
	mimeType = strings.ToLower(strings.TrimSpace(header[len("data:"):semicolon]))
	payload = strings.TrimSpace(value[comma+1:])
	if mimeType == "" || payload == "" {
		return "", "", false
	}
	return mimeType, payload, true
}

func compactImageDimensions(payload string) (width, height int, ok bool) {
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload))
	config, _, err := image.DecodeConfig(io.LimitReader(decoder, compactImageHeaderReadLimit))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

func compactImageTokenEstimate(width, height int) int {
	if width <= 0 || height <= 0 {
		return compactImageFallbackTokens
	}
	maxPixels := int64(compactImageMaxTokens) * compactImagePixelsPerToken
	if int64(width) > maxPixels/int64(height) {
		return compactImageMaxTokens
	}
	pixels := int64(width) * int64(height)
	tokens := int((pixels + compactImagePixelsPerToken - 1) / compactImagePixelsPerToken)
	if tokens < compactImageMinTokens {
		return compactImageMinTokens
	}
	if tokens > compactImageMaxTokens {
		return compactImageMaxTokens
	}
	return tokens
}

func compactBase64DecodedSize(payload string) int {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return 0
	}
	size := base64.StdEncoding.DecodedLen(len(payload))
	if strings.HasSuffix(payload, "==") {
		size -= 2
	} else if strings.HasSuffix(payload, "=") {
		size--
	}
	if size < 0 {
		return 0
	}
	return size
}

func compactMediaSHA256(value string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, value)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func truncateCompactMediaURL(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return value
	}
	return fmt.Sprintf("%s…", string(runes[:maxRunes]))
}

func cloneCompactMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}
