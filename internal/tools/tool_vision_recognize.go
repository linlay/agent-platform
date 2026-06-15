package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
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
	model, provider, err := t.models.Get(profile.ModelKey)
	if err != nil {
		return visionToolError("vision_model_not_found", err.Error(), map[string]any{"modelKey": profile.ModelKey}), nil
	}
	if !model.IsVision {
		return visionToolError("vision_model_not_vision", "configured model is not marked isVision: true", map[string]any{"modelKey": model.Key}), nil
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
	rawImages, ok := args["images"].([]any)
	if !ok || len(rawImages) == 0 {
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
		item := AnyMapNode(raw)
		referenceName := strings.TrimSpace(FirstNonEmptyString(item["reference_name"], item["referenceName"]))
		filePath := strings.TrimSpace(FirstNonEmptyString(item["file_path"], item["filePath"]))
		if (referenceName == "" && filePath == "") || (referenceName != "" && filePath != "") {
			return nil, visionToolError("vision_image_source_invalid", "each image must provide exactly one of reference_name or file_path", nil), true
		}
		var image multimodal.ImagePayload
		var err error
		if referenceName != "" {
			image, err = t.loadVisionReferenceImage(referenceName, options, execCtx)
		} else {
			image, err = t.loadVisionFileImage(filePath, options, execCtx)
		}
		if err != nil {
			if result, ok := err.(visionToolResultError); ok {
				return nil, result.result, true
			}
			return nil, visionToolError("vision_image_load_failed", err.Error(), nil), true
		}
		images = append(images, image)
	}
	return images, ToolExecutionResult{}, false
}

func (t *RuntimeToolExecutor) loadVisionReferenceImage(name string, options multimodal.ImageLoadOptions, execCtx *ExecutionContext) (multimodal.ImagePayload, error) {
	if !isPlainFileName(name) {
		return multimodal.ImagePayload{}, visionToolResultError{visionToolError("vision_reference_name_invalid", "reference_name must be a file name without path separators", map[string]any{"referenceName": name})}
	}
	chatID := ""
	if execCtx != nil {
		chatID = strings.TrimSpace(execCtx.Request.ChatID)
		if chatID == "" {
			chatID = strings.TrimSpace(execCtx.Session.ChatID)
		}
	}
	if chatID == "" || strings.TrimSpace(t.cfg.Paths.ChatsDir) == "" {
		return multimodal.ImagePayload{}, visionToolResultError{visionToolError("vision_chat_context_unavailable", "chat context is required to load reference_name images", nil)}
	}
	mimeHint := ""
	if execCtx != nil {
		for _, ref := range execCtx.Request.References {
			if strings.EqualFold(strings.TrimSpace(ref.Name), name) {
				mimeHint = ref.MimeType
				break
			}
		}
	}
	path := filepath.Join(t.cfg.Paths.ChatsDir, chatID, name)
	image, err := multimodal.LoadImageFile(path, mimeHint, options)
	if err != nil {
		return multimodal.ImagePayload{}, err
	}
	image.Name = name
	return image, nil
}

func (t *RuntimeToolExecutor) loadVisionFileImage(path string, options multimodal.ImageLoadOptions, execCtx *ExecutionContext) (multimodal.ImagePayload, error) {
	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, accessPolicySessionWithFallback(execCtx, t.cfg.FileTools.WorkingDirectory), filetools.ReadAccess, path)
	if err != nil {
		return multimodal.ImagePayload{}, visionToolResultError{visionToolError("vision_file_path_invalid", err.Error(), nil)}
	}
	if access.Blocked {
		return multimodal.ImagePayload{}, visionToolResultError{visionToolError("vision_file_path_blocked", access.Reason, map[string]any{"filePath": access.Path})}
	}
	if filetools.IsBlockedDeviceFile(access.Path) {
		return multimodal.ImagePayload{}, visionToolResultError{visionToolError("vision_file_device_blocked", "device file is blocked", map[string]any{"filePath": access.Path})}
	}
	if !access.AllowedByWhitelist && !access.AutoApproved && !filetools.ConsumeReadApproval(execCtx, access) {
		return multimodal.ImagePayload{}, visionToolResultError{fileAccessApprovalRequired("vision_recognize_approval_required", "vision_recognize read exceeds allowed roots", access)}
	}
	image, err := multimodal.LoadImageFile(access.Path, "", options)
	if err != nil {
		if errors.Is(err, multimodal.ErrUnsupportedImageMime) {
			return multimodal.ImagePayload{}, visionToolResultError{visionToolError("vision_image_unsupported", "unsupported image mime", map[string]any{"filePath": access.Path})}
		}
		if errors.Is(err, multimodal.ErrImageTooLarge) {
			return multimodal.ImagePayload{}, visionToolResultError{visionToolError("vision_image_too_large", err.Error(), map[string]any{"filePath": access.Path})}
		}
		return multimodal.ImagePayload{}, err
	}
	image.Name = filepath.Base(access.Path)
	return image, nil
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

type visionToolResultError struct {
	result ToolExecutionResult
}

func (e visionToolResultError) Error() string {
	return e.result.Error
}

func isPlainFileName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return false
	}
	if filepath.Base(name) != name {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}
