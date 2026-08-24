package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"strings"
	"time"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/modelrequest"
	"agent-platform/internal/models"
	"agent-platform/internal/multimodal"
)

const defaultVisionRecognizeMaxImages = 4

func (t *RuntimeToolExecutor) invokeVisionRecognize(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	cfg := t.cfg.VisionRecognize
	if !cfg.Enabled {
		return visionToolError("vision_recognize_disabled", "vision_recognize is disabled by configs/ai-tools.yml", nil), nil
	}
	if t.models == nil {
		return visionToolError("vision_model_registry_unavailable", "model registry is not configured for vision_recognize", nil), nil
	}
	profileName := strings.TrimSpace(AnyStringNode(args["profile"]))
	if profileName == "" {
		profileName = cfg.DefaultProfile
	}
	if profileName == "" {
		profileName = "general"
	}
	profile, ok := cfg.Profiles[profileName]
	if !ok {
		return visionToolError("vision_profile_not_found", "vision profile not found: "+profileName, map[string]any{"profile": profileName}), nil
	}
	if strings.TrimSpace(profile.ModelKey) == "" {
		return visionToolError("vision_profile_model_missing", "vision profile model-key is required: "+profileName, map[string]any{"profile": profileName}), nil
	}
	outputFormat := resolveVisionOutputFormat(AnyStringNode(args["output_format"]), profile.OutputFormat)
	prompt := strings.TrimSpace(AnyStringNode(args["prompt"]))
	if prompt == "" {
		return visionToolError("vision_prompt_required", "prompt is required", nil), nil
	}
	model, err := t.models.GetModel(profile.ModelKey)
	if err != nil {
		return visionToolError("vision_model_not_found", err.Error(), map[string]any{"modelKey": profile.ModelKey}), nil
	}
	if !models.IsVLModel(model) {
		return visionToolError("vision_model_not_vl", "configured model must use type: vl", map[string]any{"modelKey": model.Key}), nil
	}
	model, provider, err := t.models.GetVL(model.Key)
	if err != nil {
		return visionToolError("vision_model_not_found", err.Error(), map[string]any{"modelKey": profile.ModelKey}), nil
	}
	if strings.TrimSpace(provider.BaseURL) == "" || strings.TrimSpace(provider.APIKey) == "" {
		return visionToolError("vision_provider_config_invalid", "provider baseUrl and apiKey are required", map[string]any{"provider": provider.Key}), nil
	}
	images, result, handled := t.loadVisionImages(args, execCtx, profile)
	if handled {
		return result, nil
	}

	timeout := time.Duration(maxInt(profile.Timeout, 60)) * time.Second
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	content, usage, err := t.completeVisionRecognition(callCtx, model, provider, profile, outputFormat, prompt, images)
	if err != nil {
		return visionToolError("vision_model_request_failed", err.Error(), map[string]any{"modelKey": model.Key, "profile": profileName}), nil
	}
	payload := map[string]any{
		"ok":           true,
		"profile":      profileName,
		"modelKey":     model.Key,
		"outputFormat": outputFormat,
		"content":      content,
		"images":       visionImageMetadata(images),
	}
	if len(usage) > 0 {
		payload["usage"] = usage
	}
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) loadVisionImages(args map[string]any, execCtx *ExecutionContext, profile config.VisionRecognizeProfileConfig) ([]multimodal.ImagePayload, ToolExecutionResult, bool) {
	raw, exists := args["images"]
	if !exists || raw == nil {
		return nil, visionToolError("vision_images_required", "images must contain at least one item", nil), true
	}
	rawImages, ok := raw.([]any)
	if !ok {
		return nil, visionToolError(
			"vision_images_invalid_type",
			`images must be a JSON array; for one image use {"images":[{"file_path":"@chat/image.png"}]}`,
			map[string]any{
				"expectedType": "array",
				"actualType":   visionJSONType(raw),
			},
		), true
	}
	if len(rawImages) == 0 {
		return nil, visionToolError("vision_images_required", "images must contain at least one item", nil), true
	}
	maxImages := maxInt(profile.MaxImages, defaultVisionRecognizeMaxImages)
	if len(rawImages) > maxImages {
		return nil, visionToolError("vision_too_many_images", fmt.Sprintf("images exceeds max-images: %d", maxImages), map[string]any{"maxImages": maxImages}), true
	}
	options := multimodal.DefaultImageLoadOptions()
	if profile.MaxImageBytes > 0 {
		options.MaxBytes = int64(profile.MaxImageBytes)
	}
	images := make([]multimodal.ImagePayload, 0, len(rawImages))
	for _, raw := range rawImages {
		resolved, result, handled := t.resolveToolImageSource(raw, execCtx, visionImageSourcePolicy())
		if handled {
			return nil, result, true
		}
		image, err := multimodal.LoadImageFile(resolved.Path, resolved.MimeHint, options)
		if err != nil {
			if errors.Is(err, multimodal.ErrUnsupportedImageMime) {
				return nil, visionToolError("vision_image_unsupported", "unsupported image mime", map[string]any{"filePath": resolved.Path}), true
			}
			if errors.Is(err, multimodal.ErrImageTooLarge) {
				return nil, visionToolError("vision_image_too_large", err.Error(), map[string]any{"filePath": resolved.Path}), true
			}
			return nil, visionToolError("vision_image_load_failed", err.Error(), nil), true
		}
		image.Name = resolved.Name
		images = append(images, image)
	}
	return images, ToolExecutionResult{}, false
}

func visionJSONType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return "number"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func visionImageSourcePolicy() toolImageSourcePolicy {
	return toolImageSourcePolicy{
		SourceInvalidCode:        "vision_image_source_invalid",
		ReferenceNameInvalidCode: "vision_reference_name_invalid",
		ChatUnavailableCode:      "vision_chat_context_unavailable",
		FilePathInvalidCode:      "vision_file_path_invalid",
		FilePathBlockedCode:      "vision_file_path_blocked",
		DeviceBlockedCode:        "vision_file_device_blocked",
		ApprovalRequiredCode:     "vision_recognize_approval_required",
		ApprovalMessage:          "vision_recognize read exceeds allowed roots",
		Error:                    visionToolError,
	}
}

func (t *RuntimeToolExecutor) completeVisionRecognition(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, profile config.VisionRecognizeProfileConfig, outputFormat string, prompt string, images []multimodal.ImagePayload) (string, map[string]any, error) {
	switch strings.ToUpper(strings.TrimSpace(model.Protocol)) {
	case "ANTHROPIC":
		return t.completeVisionAnthropic(ctx, model, provider, profile, outputFormat, prompt, images)
	default:
		return t.completeVisionOpenAI(ctx, model, provider, profile, outputFormat, prompt, images)
	}
}

func (t *RuntimeToolExecutor) completeVisionOpenAI(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, profile config.VisionRecognizeProfileConfig, outputFormat string, prompt string, images []multimodal.ImagePayload) (string, map[string]any, error) {
	content := []map[string]any{{"type": "text", "text": visionUserPrompt(prompt, outputFormat)}}
	for _, image := range images {
		content = append(content, multimodal.OpenAIImageBlock(image))
	}
	body := map[string]any{
		"model": model.ModelID,
		"messages": []map[string]any{
			{"role": "system", "content": visionSystemPrompt(profile, outputFormat)},
			{"role": "user", "content": content},
		},
		"stream": false,
	}
	modelrequest.ApplyDeterministicTemperature(body)
	body = mergeVisionRequestCompat(body, provider, model)
	data, err := t.postVisionJSON(ctx, provider, model, body, "OPENAI")
	if err != nil {
		return "", nil, err
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", nil, err
	}
	if len(decoded.Choices) == 0 {
		return "", decoded.Usage, fmt.Errorf("vision model returned no choices")
	}
	contentText := extractVisionOpenAIContent(decoded.Choices[0].Message.Content)
	if strings.TrimSpace(contentText) == "" {
		return "", decoded.Usage, fmt.Errorf("vision model returned empty content")
	}
	return contentText, decoded.Usage, nil
}

func (t *RuntimeToolExecutor) completeVisionAnthropic(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, profile config.VisionRecognizeProfileConfig, outputFormat string, prompt string, images []multimodal.ImagePayload) (string, map[string]any, error) {
	content := []map[string]any{{"type": "text", "text": visionUserPrompt(prompt, outputFormat)}}
	for _, image := range images {
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": image.MimeType,
				"data":       image.DataBase64,
			},
		})
	}
	body := map[string]any{
		"model":      model.ModelID,
		"max_tokens": 1200,
		"system":     visionSystemPrompt(profile, outputFormat),
		"messages": []map[string]any{
			{"role": "user", "content": content},
		},
	}
	body = mergeVisionRequestCompat(body, provider, model)
	data, err := t.postVisionJSON(ctx, provider, model, body, "ANTHROPIC")
	if err != nil {
		return "", nil, err
	}
	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", nil, err
	}
	parts := make([]string, 0, len(decoded.Content))
	for _, item := range decoded.Content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "text") && strings.TrimSpace(item.Text) != "" {
			parts = append(parts, item.Text)
		}
	}
	contentText := strings.TrimSpace(strings.Join(parts, "\n"))
	if contentText == "" {
		return "", decoded.Usage, fmt.Errorf("vision model returned empty content")
	}
	return contentText, decoded.Usage, nil
}

func (t *RuntimeToolExecutor) postVisionJSON(ctx context.Context, provider models.ProviderDefinition, model models.ModelDefinition, body map[string]any, protocol string) ([]byte, error) {
	return t.postModelJSON(ctx, provider, model, body, protocol, "vision model")
}

func visionProviderEndpoint(provider models.ProviderDefinition, model models.ModelDefinition, protocol string) string {
	endpointPath := provider.Protocol(model.Protocol).EndpointPath
	if strings.TrimSpace(endpointPath) == "" {
		endpointPath = defaultVisionEndpointPath(protocol, provider.BaseURL)
	}
	return strings.TrimRight(provider.BaseURL, "/") + endpointPath
}

func defaultVisionEndpointPath(protocol string, baseURL string) string {
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "ANTHROPIC":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/messages"
		}
		return "/v1/messages"
	default:
		if normalizedBasePath(baseURL) == "/v1" {
			return "/chat/completions"
		}
		return "/v1/chat/completions"
	}
}

func normalizedBasePath(rawBaseURL string) string {
	parsed, err := neturl.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil || strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/" {
		return ""
	}
	return "/" + strings.Trim(strings.TrimSpace(parsed.Path), "/")
}

func visionProtocolHeaders(provider models.ProviderDefinition, model models.ModelDefinition, protocol string) map[string]string {
	out := map[string]string{}
	if strings.EqualFold(strings.TrimSpace(protocol), "ANTHROPIC") {
		out["anthropic-version"] = "2023-06-01"
	}
	for key, value := range provider.Protocol(model.Protocol).Headers {
		out[key] = value
	}
	for key, value := range model.Headers {
		out[key] = value
	}
	return out
}

func mergeVisionRequestCompat(body map[string]any, provider models.ProviderDefinition, model models.ModelDefinition) map[string]any {
	out := CloneMap(body)
	out = mergeVisionAnyMaps(out, AnyMapNode(AnyMapNode(provider.Protocol(model.Protocol).Compat["request"])["always"]))
	out = mergeVisionAnyMaps(out, AnyMapNode(AnyMapNode(model.Compat["request"])["always"]))
	return out
}

func mergeVisionAnyMaps(base map[string]any, overlay map[string]any) map[string]any {
	if len(overlay) == 0 {
		return base
	}
	if base == nil {
		base = map[string]any{}
	}
	for key, value := range overlay {
		if baseValue, ok := base[key].(map[string]any); ok {
			if overlayValue, ok := value.(map[string]any); ok {
				base[key] = mergeVisionAnyMaps(baseValue, overlayValue)
				continue
			}
		}
		base[key] = value
	}
	return base
}

func visionSystemPrompt(profile config.VisionRecognizeProfileConfig, outputFormat string) string {
	prompt := strings.TrimSpace(profile.SystemPrompt)
	if prompt == "" {
		prompt = "You are a visual recognition tool. Describe only observable visual facts."
	}
	if outputFormat == "json" {
		return prompt + "\nReturn valid JSON only."
	}
	return prompt
}

func visionUserPrompt(prompt string, outputFormat string) string {
	if outputFormat == "json" {
		return strings.TrimSpace(prompt) + "\nOutput format: JSON."
	}
	return strings.TrimSpace(prompt)
}

func resolveVisionOutputFormat(override string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(override)) {
	case "json":
		return "json"
	case "text":
		return "text"
	}
	switch strings.ToLower(strings.TrimSpace(fallback)) {
	case "json":
		return "json"
	default:
		return "text"
	}
}

func extractVisionOpenAIContent(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			mapped, _ := item.(map[string]any)
			if strings.EqualFold(strings.TrimSpace(AnyStringNode(mapped["type"])), "text") {
				if text := strings.TrimSpace(AnyStringNode(mapped["text"])); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func visionImageMetadata(images []multimodal.ImagePayload) []map[string]any {
	out := make([]map[string]any, 0, len(images))
	for _, image := range images {
		item := map[string]any{
			"name":      image.Name,
			"filePath":  image.FilePath,
			"mimeType":  image.MimeType,
			"sha256":    image.SHA256,
			"sizeBytes": image.SizeBytes,
			"sentBytes": image.SentBytes,
		}
		if image.Reencoded {
			item["reencoded"] = true
		}
		out = append(out, item)
	}
	return out
}

func visionToolError(code string, message string, diagnostics map[string]any) ToolExecutionResult {
	payload := map[string]any{
		"ok":      false,
		"error":   strings.TrimSpace(code),
		"message": strings.TrimSpace(message),
	}
	for key, value := range diagnostics {
		payload[key] = value
	}
	result := structuredResultWithExit(payload, -1)
	result.Error = strings.TrimSpace(code)
	return result
}
