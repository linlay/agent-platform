package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func (t *RuntimeToolExecutor) invokeImageGenerate(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	cfg := t.cfg.ImageGenerate
	if !cfg.Enabled {
		return imageGenerateToolError("image_generate_disabled", "image_generate is disabled by configs/ai-tools.yml", nil), nil
	}
	if t.models == nil {
		return imageGenerateToolError("image_generate_model_registry_unavailable", "model registry is not configured for image_generate", nil), nil
	}
	prompt := strings.TrimSpace(AnyStringNode(args["prompt"]))
	if prompt == "" {
		return imageGenerateToolError("image_generate_prompt_required", "prompt is required", nil), nil
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
		return imageGenerateToolError("image_generate_profile_not_found", "image_generate profile not found: "+profileName, map[string]any{"profile": profileName}), nil
	}
	if strings.TrimSpace(profile.ModelKey) == "" {
		return imageGenerateToolError("image_generate_profile_model_missing", "image_generate profile model-key is required: "+profileName, map[string]any{"profile": profileName}), nil
	}
	if profile.MaxPromptChars > 0 && utf8.RuneCountInString(prompt) > profile.MaxPromptChars {
		return imageGenerateToolError("image_generate_prompt_too_long", fmt.Sprintf("prompt exceeds max-prompt-chars: %d", profile.MaxPromptChars), map[string]any{"maxPromptChars": profile.MaxPromptChars}), nil
	}
	modelInfo, err := t.models.GetModel(profile.ModelKey)
	if err != nil {
		return imageGenerateToolError("image_generate_model_not_found", err.Error(), map[string]any{"modelKey": profile.ModelKey}), nil
	}
	if !models.IsImageGenerationModel(modelInfo) {
		return imageGenerateToolError("image_generate_model_not_image_generation", "configured model is not type: image-generation", map[string]any{"modelKey": modelInfo.Key, "type": modelInfo.Type}), nil
	}
	model, provider, err := t.models.GetImageGeneration(profile.ModelKey)
	if err != nil {
		return imageGenerateToolError("image_generate_model_not_found", err.Error(), map[string]any{"modelKey": profile.ModelKey}), nil
	}
	if strings.TrimSpace(provider.BaseURL) == "" || strings.TrimSpace(provider.APIKey) == "" {
		return imageGenerateToolError("image_generate_provider_config_invalid", "provider baseUrl and apiKey are required", map[string]any{"provider": provider.Key}), nil
	}

	size := strings.TrimSpace(FirstNonEmptyString(args["size"], profile.Size, model.Image.DefaultSize))
	if size == "" {
		size = "1024x1024"
	}
	responseFormat, ok := resolveImageGenerateResponseFormat(AnyStringNode(args["response_format"]), profile.ResponseFormat, model.Image.ResponseFormats)
	if !ok {
		return imageGenerateToolError("image_generate_response_format_invalid", "response_format must be b64_json or url", nil), nil
	}
	n := AnyIntNode(args["n"])
	if n <= 0 {
		n = 1
	}
	if n > 4 {
		return imageGenerateToolError("image_generate_n_invalid", "n must be between 1 and 4", map[string]any{"n": n}), nil
	}

	body := map[string]any{
		"model":           model.ModelID,
		"prompt":          prompt,
		"size":            size,
		"response_format": responseFormat,
		"n":               n,
	}
	body = mergeVisionRequestCompat(body, provider, model)

	start := time.Now()
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(imageGenerateTimeout(model, profile))*time.Second)
	defer cancel()
	decoded, err := t.completeImageGenerate(callCtx, model, provider, profile, body)
	if err != nil {
		return imageGenerateToolError("image_generate_model_request_failed", err.Error(), map[string]any{"modelKey": model.Key, "profile": profileName}), nil
	}
	images, err := t.materializeGeneratedImages(decoded.Data, profile, execCtx)
	if err != nil {
		return imageGenerateToolError("image_generate_model_response_invalid", err.Error(), map[string]any{"modelKey": model.Key, "profile": profileName}), nil
	}
	if len(images) == 0 {
		return imageGenerateToolError("image_generate_model_response_invalid", "model returned no image data", map[string]any{"modelKey": model.Key, "profile": profileName}), nil
	}

	payload := map[string]any{
		"ok":             true,
		"profile":        profileName,
		"modelKey":       model.Key,
		"size":           size,
		"responseFormat": responseFormat,
		"images":         images,
		"durationMs":     time.Since(start).Milliseconds(),
	}
	if decoded.Created > 0 {
		payload["rawCreated"] = decoded.Created
	}
	if len(decoded.Usage) > 0 {
		payload["usage"] = decoded.Usage
	}
	return structuredResult(payload), nil
}

type imageGenerateResponse struct {
	Created int64                     `json:"created"`
	Data    []imageGenerateData       `json:"data"`
	Usage   map[string]any            `json:"usage"`
	Error   *imageGenerateErrorDetail `json:"error"`
}

type imageGenerateData struct {
	URL           string `json:"url"`
	B64JSON       string `json:"b64_json"`
	RevisedPrompt string `json:"revised_prompt"`
}

type imageGenerateErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

func (t *RuntimeToolExecutor) completeImageGenerate(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, profile config.ImageGenerateProfileConfig, body map[string]any) (imageGenerateResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return imageGenerateResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, imageGenerateEndpoint(provider, model, profile), bytes.NewReader(payload))
	if err != nil {
		return imageGenerateResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
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
		return imageGenerateResponse{}, fmt.Errorf("image generation request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded imageGenerateResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return imageGenerateResponse{}, err
	}
	if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
		return imageGenerateResponse{}, fmt.Errorf("%s", decoded.Error.Message)
	}
	return decoded, nil
}

func imageGenerateEndpoint(provider models.ProviderDefinition, model models.ModelDefinition, profile config.ImageGenerateProfileConfig) string {
	endpoint := strings.TrimSpace(profile.EndpointPath)
	if endpoint == "" {
		endpoint = strings.TrimSpace(model.Image.EndpointPath)
	}
	if endpoint == "" {
		endpoint = defaultImageGenerateEndpointPath(provider.BaseURL)
	}
	if parsed, err := neturl.Parse(endpoint); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return endpoint
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return strings.TrimRight(provider.BaseURL, "/") + endpoint
}

func imageGenerateTimeout(model models.ModelDefinition, profile config.ImageGenerateProfileConfig) int {
	if profile.Timeout > 0 {
		return profile.Timeout
	}
	if model.Image.Timeout > 0 {
		return model.Image.Timeout
	}
	return 120
}

func defaultImageGenerateEndpointPath(baseURL string) string {
	if normalizedBasePath(baseURL) == "/v1" {
		return "/images/generations"
	}
	return "/v1/images/generations"
}

func resolveImageGenerateResponseFormat(override string, fallback string, allowed []string) (string, bool) {
	var parsed string
	if strings.TrimSpace(override) != "" {
		value, ok := parseImageGenerateResponseFormat(override)
		if !ok {
			return "", false
		}
		parsed = value
	} else if value, ok := parseImageGenerateResponseFormat(fallback); ok {
		parsed = value
	} else {
		parsed = "b64_json"
	}
	if len(allowed) == 0 {
		return parsed, true
	}
	for _, item := range allowed {
		if strings.EqualFold(strings.TrimSpace(item), parsed) {
			return parsed, true
		}
	}
	return "", false
}

func parseImageGenerateResponseFormat(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "b64_json", "":
		return "b64_json", true
	case "url":
		return "url", true
	default:
		return "", false
	}
}

func (t *RuntimeToolExecutor) materializeGeneratedImages(items []imageGenerateData, profile config.ImageGenerateProfileConfig, execCtx *ExecutionContext) ([]map[string]any, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("model returned empty data")
	}
	images := make([]map[string]any, 0, len(items))
	for index, item := range items {
		image := map[string]any{
			"index": index,
		}
		if text := strings.TrimSpace(item.RevisedPrompt); text != "" {
			image["revisedPrompt"] = text
		}
		if rawURL := strings.TrimSpace(item.URL); rawURL != "" {
			image["url"] = rawURL
			images = append(images, image)
			continue
		}
		if strings.TrimSpace(item.B64JSON) == "" {
			return nil, fmt.Errorf("image item %d has neither url nor b64_json", index)
		}
		data, imageMime, err := decodeGeneratedImageBase64(item.B64JSON, profile.OutputMimeType)
		if err != nil {
			return nil, fmt.Errorf("decode image item %d: %w", index, err)
		}
		image["mimeType"] = imageMime
		image["sizeBytes"] = len(data)
		sum := sha256.Sum256(data)
		image["sha256"] = hex.EncodeToString(sum[:])
		if profile.PersistArtifact {
			artifact, err := persistGeneratedImageArtifact(t.cfg.Paths.ChatsDir, execCtx, data, imageMime, index)
			if err != nil {
				return nil, err
			}
			for key, value := range artifact {
				image[key] = value
			}
		} else {
			image["b64Json"] = base64.StdEncoding.EncodeToString(data)
		}
		images = append(images, image)
	}
	return images, nil
}

func decodeGeneratedImageBase64(raw string, fallbackMime string) ([]byte, string, error) {
	value := strings.TrimSpace(raw)
	imageMime := normalizeGeneratedImageMime(fallbackMime)
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		header, payload, ok := strings.Cut(value, ",")
		if !ok {
			return nil, "", fmt.Errorf("invalid data URL")
		}
		if !strings.Contains(strings.ToLower(header), ";base64") {
			return nil, "", fmt.Errorf("data URL is not base64 encoded")
		}
		mimeValue := strings.TrimPrefix(header, "data:")
		if marker := strings.Index(mimeValue, ";"); marker >= 0 {
			mimeValue = mimeValue[:marker]
		}
		imageMime = normalizeGeneratedImageMime(mimeValue)
		value = strings.TrimSpace(payload)
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(value)
	}
	if err != nil {
		return nil, "", err
	}
	if imageMime == "" {
		imageMime = "image/png"
	}
	return data, imageMime, nil
}

func normalizeGeneratedImageMime(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "image/jpg" {
		return "image/jpeg"
	}
	if strings.HasPrefix(value, "image/") {
		return value
	}
	return "image/png"
}

func persistGeneratedImageArtifact(chatsRoot string, execCtx *ExecutionContext, data []byte, imageMime string, index int) (map[string]any, error) {
	chatID, runID := imageGenerateChatRun(execCtx)
	if strings.TrimSpace(chatsRoot) == "" || strings.TrimSpace(chatID) == "" {
		return nil, fmt.Errorf("chat context is required to persist generated images")
	}
	if strings.TrimSpace(runID) == "" {
		runID = "manual"
	}
	runID = safeGeneratedImageNameSegment(runID)
	chatDir := filepath.Join(chatsRoot, chatID)
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return nil, err
	}
	ext := generatedImageExtension(imageMime)
	timestamp := time.Now().UnixMilli()
	name := fmt.Sprintf("image_generate_%s_%d_%d%s", runID, timestamp, index, ext)
	targetPath := filepath.Join(chatDir, name)
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		return nil, err
	}
	relativePath, err := filepath.Rel(chatDir, targetPath)
	if err != nil || isPathOutsideBase(relativePath) {
		return nil, fmt.Errorf("generated image escaped chat directory")
	}
	relativePath = filepath.ToSlash(relativePath)
	return map[string]any{
		"artifactId":   fmt.Sprintf("image_generate_%d_%d", timestamp, index),
		"name":         filepath.Base(targetPath),
		"path":         targetPath,
		"relativePath": relativePath,
		"url":          artifactResourceURL(chatID, relativePath),
		"type":         "image",
	}, nil
}

func safeGeneratedImageNameSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "manual"
	}
	return strings.NewReplacer("/", "_", `\`, "_").Replace(value)
}

func imageGenerateChatRun(execCtx *ExecutionContext) (string, string) {
	if execCtx == nil {
		return "", ""
	}
	chatID := strings.TrimSpace(execCtx.Session.ChatID)
	if chatID == "" {
		chatID = strings.TrimSpace(execCtx.Request.ChatID)
	}
	runID := strings.TrimSpace(execCtx.Session.RunID)
	if runID == "" {
		runID = strings.TrimSpace(execCtx.Request.RunID)
	}
	return chatID, runID
}

func generatedImageExtension(imageMime string) string {
	extensions, _ := mime.ExtensionsByType(strings.ToLower(strings.TrimSpace(imageMime)))
	if len(extensions) > 0 {
		return extensions[0]
	}
	switch strings.ToLower(strings.TrimSpace(imageMime)) {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func imageGenerateToolError(code string, message string, diagnostics map[string]any) ToolExecutionResult {
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
