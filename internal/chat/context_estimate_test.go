package chat

import (
	"strings"
	"testing"
)

const compactTestOnePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="

func TestCompactMediaEstimateUsesDimensionsAndBounds(t *testing.T) {
	messages := []map[string]any{{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "inspect"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + compactTestOnePixelPNG}},
		},
	}}
	projected, mediaTokens := projectCompactMediaMessages(messages, true)
	if mediaTokens != compactImageMinTokens {
		t.Fatalf("media tokens = %d, want %d", mediaTokens, compactImageMinTokens)
	}
	content, _ := projected[0]["content"].([]any)
	imageBlock, _ := content[1].(map[string]any)
	metadata, _ := imageBlock["image_reference"].(map[string]any)
	if imageBlock["type"] != "image_reference" || metadata["width"] != 1 || metadata["height"] != 1 || metadata["sizeBytes"] != 68 {
		t.Fatalf("unexpected projection %#v", imageBlock)
	}
	if !strings.HasPrefix(stringFromAny(metadata["payloadSha256"]), "sha256:") {
		t.Fatalf("missing payload digest %#v", metadata)
	}
	if got := compactImageTokenEstimate(1000, 750); got != 1000 {
		t.Fatalf("dimension estimate = %d, want 1000", got)
	}
	if got, want := compactImageTokenEstimate(5237, 3581), (5237*3581+compactImagePixelsPerToken-1)/compactImagePixelsPerToken; got != want {
		t.Fatalf("original image dimension estimate = %d, want %d", got, want)
	}
	if got := compactImageTokenEstimate(100000, 100000); got != compactImageMaxTokens {
		t.Fatalf("large dimension estimate = %d, want clamp %d", got, compactImageMaxTokens)
	}
}

func TestCompactMediaEstimateDoesNotCountBase64AsText(t *testing.T) {
	payload := strings.Repeat("A", 4_271_740)
	dataURL := "data:image/jpeg;base64," + payload
	messages := []map[string]any{{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "extract the table"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL}},
		},
	}}

	if got := EstimateRawMessageTokens(messages); got < compactImageFallbackTokens || got > compactImageFallbackTokens+1000 {
		t.Fatalf("media-safe estimate = %d, want bounded fallback", got)
	}
	prompt, err := BuildCompactPromptWithinBudget(messages, 10_000)
	if err != nil {
		t.Fatalf("BuildCompactPromptWithinBudget: %v", err)
	}
	for _, forbidden := range []string{"data:image/jpeg;base64", strings.Repeat("A", 128)} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("compact prompt retained opaque media payload")
		}
	}
	for _, required := range []string{"image_reference", "image/jpeg", "sizeBytes", "payloadSha256"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("compact prompt missing %q: %s", required, prompt)
		}
	}
	content, _ := messages[0]["content"].([]any)
	imageBlock, _ := content[1].(map[string]any)
	imageURL, _ := imageBlock["image_url"].(map[string]any)
	if imageURL["url"] != dataURL {
		t.Fatal("media projection mutated the source message")
	}
}

func TestCompactMediaEstimateSumsImagesAndFallsBackWithoutDimensions(t *testing.T) {
	messages := []map[string]any{{
		"role": "user",
		"content": []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + compactTestOnePixelPNG}},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/image.png"}},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/webp;base64,AAAA"}},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png,not-base64"}},
		},
	}}
	_, mediaTokens := projectCompactMediaMessages(messages, false)
	want := compactImageMinTokens + compactImageFallbackTokens*3
	if mediaTokens != want {
		t.Fatalf("media tokens = %d, want %d", mediaTokens, want)
	}
}
