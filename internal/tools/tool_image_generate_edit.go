package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	neturl "net/url"
	"path/filepath"
	"strconv"
	"strings"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/multimodal"
)

const (
	defaultImageGenerateMaxImages     = 4
	defaultImageGenerateMaxImageBytes = 20 << 20
	maxImageGenerateMaskPixels        = 100_000_000
)

type imageGenerateMaskPayload struct {
	Name string
	Data []byte
}

func (t *RuntimeToolExecutor) loadImageGenerateInputs(args map[string]any, execCtx *ExecutionContext, profile config.ImageGenerateProfileConfig, model models.ModelDefinition) ([]multimodal.ImagePayload, *imageGenerateMaskPayload, ToolExecutionResult, bool) {
	rawMask, maskProvided := args["mask"]
	if maskProvided && rawMask == nil {
		maskProvided = false
	}
	rawImages, imagesProvided := args["images"]
	if !imagesProvided || rawImages == nil {
		if maskProvided {
			return nil, nil, imageGenerateToolError("image_generate_mask_requires_images", "mask requires images and applies to images[0]", nil), true
		}
		return nil, nil, ToolExecutionResult{}, false
	}
	imageItems, ok := rawImages.([]any)
	if !ok || len(imageItems) == 0 {
		return nil, nil, imageGenerateToolError("image_generate_images_invalid", "images must contain at least one item", nil), true
	}
	maxImages := profile.MaxImages
	if maxImages <= 0 || maxImages > defaultImageGenerateMaxImages {
		maxImages = defaultImageGenerateMaxImages
	}
	if len(imageItems) > maxImages {
		return nil, nil, imageGenerateToolError("image_generate_too_many_images", fmt.Sprintf("images exceeds max-images: %d", maxImages), map[string]any{"maxImages": maxImages}), true
	}

	edit := model.Image.Edit
	if strings.TrimSpace(edit.RequestFormat) == "" {
		return nil, nil, imageGenerateToolError("image_generate_edit_unsupported", "selected image model does not declare image.edit support", map[string]any{"modelKey": model.Key}), true
	}
	if err := models.ValidateModelImageEditConfig(edit); err != nil {
		return nil, nil, imageGenerateToolError("image_generate_edit_config_invalid", err.Error(), map[string]any{"modelKey": model.Key}), true
	}
	if maskProvided && !strings.EqualFold(strings.TrimSpace(edit.MaskProtocol), models.ImageMaskProtocolOpenAIAlpha) {
		return nil, nil, imageGenerateToolError("image_generate_mask_unsupported", "selected image model does not support native mask/inpainting", map[string]any{"modelKey": model.Key}), true
	}

	options := multimodal.DefaultImageLoadOptions()
	options.ReencodeThresholdBytes = 0
	options.MaxBytes = int64(profile.MaxImageBytes)
	if options.MaxBytes <= 0 {
		options.MaxBytes = defaultImageGenerateMaxImageBytes
	}
	images := make([]multimodal.ImagePayload, 0, len(imageItems))
	for _, raw := range imageItems {
		imagePayload, result, handled := t.loadPreservedImageGenerateSource(raw, execCtx, options)
		if handled {
			return nil, nil, result, true
		}
		images = append(images, imagePayload)
	}
	if !maskProvided {
		return images, nil, ToolExecutionResult{}, false
	}
	maskNode := AnyMapNode(rawMask)
	mode := strings.ToLower(strings.TrimSpace(AnyStringNode(maskNode["mode"])))
	switch mode {
	case "alpha", "white_edit", "black_edit":
	default:
		return nil, nil, imageGenerateToolError("image_generate_mask_mode_invalid", "mask.mode must be alpha, white_edit, or black_edit", nil), true
	}
	maskImage, result, handled := t.loadPreservedImageGenerateSource(rawMask, execCtx, options)
	if handled {
		return nil, nil, result, true
	}
	targetData, err := base64.StdEncoding.DecodeString(images[0].DataBase64)
	if err != nil {
		return nil, nil, imageGenerateToolError("image_generate_image_load_failed", "decode images[0]: "+err.Error(), nil), true
	}
	maskData, err := base64.StdEncoding.DecodeString(maskImage.DataBase64)
	if err != nil {
		return nil, nil, imageGenerateToolError("image_generate_mask_invalid", "decode mask: "+err.Error(), nil), true
	}
	normalized, err := normalizeImageGenerateMask(targetData, maskData, maskImage.MimeType, mode)
	if err != nil {
		return nil, nil, imageGenerateToolError("image_generate_mask_invalid", err.Error(), nil), true
	}
	return images, &imageGenerateMaskPayload{Name: normalizedMaskFilename(maskImage.Name), Data: normalized}, ToolExecutionResult{}, false
}

func (t *RuntimeToolExecutor) loadPreservedImageGenerateSource(raw any, execCtx *ExecutionContext, options multimodal.ImageLoadOptions) (multimodal.ImagePayload, ToolExecutionResult, bool) {
	resolved, result, handled := t.resolveToolImageSource(raw, execCtx, imageGenerateSourcePolicy())
	if handled {
		return multimodal.ImagePayload{}, result, true
	}
	imagePayload, err := multimodal.LoadImageFile(resolved.Path, "", options)
	if err != nil {
		switch {
		case errors.Is(err, multimodal.ErrUnsupportedImageMime):
			return multimodal.ImagePayload{}, imageGenerateToolError("image_generate_image_unsupported", "unsupported image mime", map[string]any{"filePath": resolved.Path}), true
		case errors.Is(err, multimodal.ErrImageTooLarge):
			return multimodal.ImagePayload{}, imageGenerateToolError("image_generate_image_too_large", err.Error(), map[string]any{"filePath": resolved.Path}), true
		default:
			return multimodal.ImagePayload{}, imageGenerateToolError("image_generate_image_load_failed", err.Error(), map[string]any{"filePath": resolved.Path}), true
		}
	}
	imagePayload.Name = resolved.Name
	return imagePayload, ToolExecutionResult{}, false
}

func imageGenerateSourcePolicy() toolImageSourcePolicy {
	return toolImageSourcePolicy{
		SourceInvalidCode:        "image_generate_image_source_invalid",
		ReferenceNameInvalidCode: "image_generate_reference_name_invalid",
		ChatUnavailableCode:      "image_generate_chat_context_unavailable",
		FilePathInvalidCode:      "image_generate_file_path_invalid",
		FilePathBlockedCode:      "image_generate_file_path_blocked",
		DeviceBlockedCode:        "image_generate_file_device_blocked",
		ApprovalRequiredCode:     "image_generate_approval_required",
		ApprovalMessage:          "image_generate read exceeds allowed roots",
		Error:                    imageGenerateToolError,
	}
}

func normalizeImageGenerateMask(targetData []byte, maskData []byte, maskMime string, mode string) ([]byte, error) {
	targetConfig, _, err := image.DecodeConfig(bytes.NewReader(targetData))
	if err != nil {
		return nil, fmt.Errorf("images[0] dimensions cannot be decoded: %w", err)
	}
	maskConfig, _, err := image.DecodeConfig(bytes.NewReader(maskData))
	if err != nil {
		return nil, fmt.Errorf("mask dimensions cannot be decoded: %w", err)
	}
	if targetConfig.Width != maskConfig.Width || targetConfig.Height != maskConfig.Height {
		return nil, fmt.Errorf("mask dimensions %dx%d must match images[0] dimensions %dx%d", maskConfig.Width, maskConfig.Height, targetConfig.Width, targetConfig.Height)
	}
	if maskConfig.Width <= 0 || maskConfig.Height <= 0 || int64(maskConfig.Width)*int64(maskConfig.Height) > maxImageGenerateMaskPixels {
		return nil, fmt.Errorf("mask dimensions exceed the supported pixel limit")
	}
	if mode == "alpha" {
		if !strings.EqualFold(strings.TrimSpace(maskMime), "image/png") {
			return nil, fmt.Errorf("alpha mask must be a PNG image")
		}
		if !pngCarriesAlpha(maskData) {
			return nil, fmt.Errorf("alpha mask PNG must contain an alpha channel")
		}
	}
	maskImage, _, err := image.Decode(bytes.NewReader(maskData))
	if err != nil {
		return nil, fmt.Errorf("decode mask: %w", err)
	}
	output := image.NewNRGBA(image.Rect(0, 0, maskConfig.Width, maskConfig.Height))
	bounds := maskImage.Bounds()
	for y := 0; y < maskConfig.Height; y++ {
		for x := 0; x < maskConfig.Width; x++ {
			source := maskImage.At(bounds.Min.X+x, bounds.Min.Y+y)
			var alpha uint8
			switch mode {
			case "alpha":
				_, _, _, a := source.RGBA()
				alpha = uint8(a >> 8)
			case "white_edit":
				alpha = 255 - imageMaskLuminance(source)
			case "black_edit":
				alpha = imageMaskLuminance(source)
			}
			output.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: alpha})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, output); err != nil {
		return nil, fmt.Errorf("encode normalized mask: %w", err)
	}
	return encoded.Bytes(), nil
}

func imageMaskLuminance(value color.Color) uint8 {
	r, g, b, _ := value.RGBA()
	r8, g8, b8 := r>>8, g>>8, b>>8
	return uint8((299*r8 + 587*g8 + 114*b8 + 500) / 1000)
}

func pngCarriesAlpha(data []byte) bool {
	if len(data) < 33 || !bytes.Equal(data[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return false
	}
	colorType := data[25]
	if colorType == 4 || colorType == 6 {
		return true
	}
	for offset := 8; offset+12 <= len(data); {
		length := int(data[offset])<<24 | int(data[offset+1])<<16 | int(data[offset+2])<<8 | int(data[offset+3])
		if length < 0 || offset+12+length > len(data) {
			return false
		}
		chunkType := string(data[offset+4 : offset+8])
		if chunkType == "tRNS" {
			return true
		}
		if chunkType == "IEND" {
			break
		}
		offset += 12 + length
	}
	return false
}

func normalizedMaskFilename(name string) string {
	base := strings.TrimSuffix(filepath.Base(strings.TrimSpace(name)), filepath.Ext(name))
	if base == "" {
		base = "mask"
	}
	return base + ".png"
}

func (t *RuntimeToolExecutor) completeImageGenerateEdit(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, prompt string, size string, responseFormat string, n int, images []multimodal.ImagePayload, mask *imageGenerateMaskPayload) (imageGenerateResponse, error) {
	switch strings.ToLower(strings.TrimSpace(model.Image.Edit.RequestFormat)) {
	case models.ImageEditRequestFormatOpenAIMultipart:
		return t.completeImageGenerateMultipartEdit(ctx, model, provider, prompt, size, responseFormat, n, images, mask)
	case models.ImageEditRequestFormatOpenAIChatCompletions:
		if mask != nil {
			return imageGenerateResponse{}, fmt.Errorf("mask is not supported by openai-chat-completions image editing")
		}
		return t.completeImageGenerateChatEdit(ctx, model, provider, prompt, size, responseFormat, n, images)
	default:
		return imageGenerateResponse{}, fmt.Errorf("unsupported image edit request format %q", model.Image.Edit.RequestFormat)
	}
}

func (t *RuntimeToolExecutor) completeImageGenerateMultipartEdit(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, prompt string, size string, responseFormat string, n int, images []multimodal.ImagePayload, mask *imageGenerateMaskPayload) (imageGenerateResponse, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"model":           model.ModelID,
		"prompt":          prompt,
		"size":            size,
		"response_format": responseFormat,
		"n":               strconv.Itoa(n),
	} {
		if err := writer.WriteField(key, value); err != nil {
			return imageGenerateResponse{}, err
		}
	}
	for _, imagePayload := range images {
		data, err := base64.StdEncoding.DecodeString(imagePayload.DataBase64)
		if err != nil {
			return imageGenerateResponse{}, fmt.Errorf("decode input image %s: %w", imagePayload.Name, err)
		}
		if err := writeImageGenerateMultipartFile(writer, "image[]", imagePayload.Name, imagePayload.MimeType, data); err != nil {
			return imageGenerateResponse{}, err
		}
	}
	if mask != nil {
		if err := writeImageGenerateMultipartFile(writer, "mask", mask.Name, "image/png", mask.Data); err != nil {
			return imageGenerateResponse{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return imageGenerateResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, imageGenerateEditEndpoint(provider, model), &body)
	if err != nil {
		return imageGenerateResponse{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return t.doImageGenerateRequest(req, model, provider, "image edit")
}

func writeImageGenerateMultipartFile(writer *multipart.Writer, fieldName string, filename string, contentType string, data []byte) error {
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." {
		filename = "image" + generatedImageExtension(contentType)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": fieldName, "filename": filename}))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

func (t *RuntimeToolExecutor) completeImageGenerateChatEdit(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, prompt string, size string, responseFormat string, n int, images []multimodal.ImagePayload) (imageGenerateResponse, error) {
	content := []map[string]any{{"type": "text", "text": prompt}}
	for _, imagePayload := range images {
		content = append(content, multimodal.OpenAIImageBlock(imagePayload))
	}
	body := map[string]any{
		"model": model.ModelID,
		"messages": []map[string]any{
			{"role": "user", "content": content},
		},
		"modalities":      []string{"text", "image"},
		"size":            size,
		"response_format": responseFormat,
		"n":               n,
	}
	body = mergeVisionRequestCompat(body, provider, model)
	payload, err := json.Marshal(body)
	if err != nil {
		return imageGenerateResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, imageGenerateEditEndpoint(provider, model), bytes.NewReader(payload))
	if err != nil {
		return imageGenerateResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return t.doImageGenerateRequest(req, model, provider, "image edit")
}

func (t *RuntimeToolExecutor) doImageGenerateRequest(req *http.Request, model models.ModelDefinition, provider models.ProviderDefinition, operation string) (imageGenerateResponse, error) {
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	for key, value := range visionProtocolHeaders(provider, model, model.Protocol) {
		req.Header.Set(key, value)
	}
	client := t.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return imageGenerateResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return imageGenerateResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return imageGenerateResponse{}, fmt.Errorf("%s request failed with status %d: %s", operation, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	decoded, err := decodeImageGenerateResponse(data)
	if err != nil {
		return imageGenerateResponse{}, err
	}
	if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		return imageGenerateResponse{}, fmt.Errorf("%s", decoded.Error.Message)
	}
	return decoded, nil
}

func imageGenerateEditEndpoint(provider models.ProviderDefinition, model models.ModelDefinition) string {
	endpoint := strings.TrimSpace(model.Image.Edit.EndpointPath)
	if parsed, err := neturl.Parse(endpoint); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return endpoint
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return strings.TrimRight(provider.BaseURL, "/") + endpoint
}

func decodeImageGenerateResponse(data []byte) (imageGenerateResponse, error) {
	var decoded imageGenerateResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return imageGenerateResponse{}, err
	}
	if len(decoded.Data) > 0 || decoded.Error != nil {
		return decoded, nil
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		return imageGenerateResponse{}, err
	}
	seen := map[string]bool{}
	choices, _ := envelope["choices"].([]any)
	for _, rawChoice := range choices {
		message := AnyMapNode(AnyMapNode(rawChoice)["message"])
		for _, key := range []string{"images", "content"} {
			extractImageGenerateCandidates(message[key], &decoded.Data, seen)
		}
	}
	return decoded, nil
}

func extractImageGenerateCandidates(value any, output *[]imageGenerateData, seen map[string]bool) {
	switch typed := value.(type) {
	case string:
		candidate := strings.TrimSpace(typed)
		if strings.HasPrefix(strings.ToLower(candidate), "data:image/") {
			appendImageGenerateCandidate(imageGenerateData{B64JSON: candidate}, output, seen)
		} else if parsed, err := neturl.Parse(candidate); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
			appendImageGenerateCandidate(imageGenerateData{URL: candidate}, output, seen)
		}
	case []any:
		for _, item := range typed {
			extractImageGenerateCandidates(item, output, seen)
		}
	case map[string]any:
		if b64 := strings.TrimSpace(AnyStringNode(typed["b64_json"])); b64 != "" {
			appendImageGenerateCandidate(imageGenerateData{B64JSON: b64}, output, seen)
		}
		if rawURL := typed["url"]; rawURL != nil {
			extractImageGenerateCandidates(rawURL, output, seen)
		}
		for _, key := range []string{"image_url", "inline_data", "data", "images", "content"} {
			if nested := typed[key]; nested != nil {
				if key == "inline_data" {
					inline := AnyMapNode(nested)
					rawData := strings.TrimSpace(AnyStringNode(inline["data"]))
					mimeType := strings.TrimSpace(FirstNonEmptyString(inline["mime_type"], inline["mimeType"]))
					if rawData != "" && strings.HasPrefix(strings.ToLower(mimeType), "image/") {
						appendImageGenerateCandidate(imageGenerateData{B64JSON: "data:" + mimeType + ";base64," + rawData}, output, seen)
						continue
					}
				}
				extractImageGenerateCandidates(nested, output, seen)
			}
		}
	}
}

func appendImageGenerateCandidate(candidate imageGenerateData, output *[]imageGenerateData, seen map[string]bool) {
	key := strings.TrimSpace(candidate.URL)
	if key == "" {
		key = strings.TrimSpace(candidate.B64JSON)
	}
	if key == "" || seen[key] {
		return
	}
	seen[key] = true
	*output = append(*output, candidate)
}
