package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestImageGenerateDisabled(t *testing.T) {
	executor := &RuntimeToolExecutor{cfg: config.Config{}}
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_disabled" {
		t.Fatalf("expected disabled error, got %#v", result)
	}
}

func TestImageGenerateMissingProfile(t *testing.T) {
	registry := writeImageGenerateRegistry(t, "http://127.0.0.1:1", true)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, "")
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt":  "draw",
		"profile": "missing",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_profile_not_found" {
		t.Fatalf("expected missing profile error, got %#v", result)
	}
}

func TestImageGenerateMissingModel(t *testing.T) {
	registry := writeImageGenerateRegistry(t, "http://127.0.0.1:1", true)
	cfg := defaultImageGenerateTestConfig()
	cfg.Profiles["general"] = config.ImageGenerateProfileConfig{
		ModelKey:        "missing-model",
		Timeout:         120,
		Size:            "1024x1024",
		ResponseFormat:  "b64_json",
		OutputMimeType:  "image/png",
		MaxPromptChars:  4000,
		PersistArtifact: true,
	}
	executor := imageGenerateTestExecutor(cfg, registry, "")
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "draw",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_model_not_found" {
		t.Fatalf("expected missing model error, got %#v", result)
	}
}

func TestImageGenerateProviderConfigInvalid(t *testing.T) {
	registry := writeImageGenerateRegistry(t, "http://127.0.0.1:1", false)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, "")
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "draw",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_provider_config_invalid" {
		t.Fatalf("expected provider config error, got %#v", result)
	}
}

func TestImageGenerateRejectsNonImageModel(t *testing.T) {
	registry := writeImageGenerateRegistryWithType(t, "http://127.0.0.1:1", true, models.ModelTypeChat)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, "")
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "draw",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_model_not_image_generation" {
		t.Fatalf("expected model type error, got %#v", result)
	}
}

func TestImageGenerateUsesModelImageDefaults(t *testing.T) {
	var captured map[string]any
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/images" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		_, _ = w.Write([]byte(`{"data":[{"url":"https://cdn.example/model-default.png"}]}`))
	}))
	defer modelServer.Close()

	registry := writeImageGenerateRegistryWithImageConfig(t, modelServer.URL, true, []string{
		"  endpointPath: /custom/images",
		"  timeout: 3",
		"  defaultSize: 768x768",
		"  responseFormats:",
		"    - url",
	})
	cfg := defaultImageGenerateTestConfig()
	cfg.Profiles["general"] = config.ImageGenerateProfileConfig{
		ModelKey:        "image-model",
		ResponseFormat:  "url",
		OutputMimeType:  "image/png",
		MaxPromptChars:  4000,
		PersistArtifact: true,
	}
	executor := imageGenerateTestExecutor(cfg, registry, t.TempDir())
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "draw",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"}})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("expected successful result, got %#v", result)
	}
	if captured["size"] != "768x768" || captured["response_format"] != "url" {
		t.Fatalf("expected model image defaults in request, got %#v", captured)
	}
}

func TestImageGenerateB64ResponsePersistsArtifact(t *testing.T) {
	imageBytes := []byte("fake image bytes")
	encoded := base64.StdEncoding.EncodeToString(imageBytes)
	var captured map[string]any
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"` + encoded + `","revised_prompt":"clearer prompt"}],"usage":{"total_tokens":7}}`))
	}))
	defer modelServer.Close()

	chatsRoot := t.TempDir()
	registry := writeImageGenerateRegistry(t, modelServer.URL, true)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, chatsRoot)
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt":          "draw a tiny robot",
		"size":            "512x512",
		"response_format": "b64_json",
		"n":               2,
	}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"},
	})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("expected successful result, got %#v", result)
	}
	if captured["model"] != "image-model-id" ||
		captured["prompt"] != "draw a tiny robot" ||
		captured["size"] != "512x512" ||
		captured["response_format"] != "b64_json" ||
		captured["n"] != float64(2) {
		t.Fatalf("unexpected request body: %#v", captured)
	}
	images, ok := result.Structured["images"].([]map[string]any)
	if !ok || len(images) != 1 {
		t.Fatalf("expected one image result, got %#v", result.Structured["images"])
	}
	image := images[0]
	path := contracts.AnyStringNode(image["path"])
	if path == "" || filepath.Dir(path) != filepath.Join(chatsRoot, "chat-1") {
		t.Fatalf("expected persisted image in chat root, got %#v", image)
	}
	filename := filepath.Base(path)
	if !strings.HasPrefix(filename, "image_generate_run-1_") {
		t.Fatalf("expected filename to include run ID, got %q", filename)
	}
	relativePath := contracts.AnyStringNode(image["relativePath"])
	if relativePath != filename || strings.Contains(relativePath, "/") {
		t.Fatalf("expected root relative path, got %#v", image)
	}
	decodedURL, err := neturl.QueryUnescape(strings.TrimPrefix(contracts.AnyStringNode(image["url"]), "/api/resource?file="))
	if err != nil {
		t.Fatalf("decode image url: %v", err)
	}
	if decodedURL != filepath.ToSlash(filepath.Join("chat-1", filename)) {
		t.Fatalf("expected URL to target chat root file, got %q", decodedURL)
	}
	if _, err := os.Stat(filepath.Join(chatsRoot, "chat-1", "artifacts", "run-1")); !os.IsNotExist(err) {
		t.Fatalf("did not expect artifact directory, stat err=%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted artifact: %v", err)
	}
	if string(data) != string(imageBytes) {
		t.Fatalf("unexpected persisted artifact bytes: %q", string(data))
	}
	sum := sha256.Sum256(imageBytes)
	if image["sha256"] != hex.EncodeToString(sum[:]) ||
		image["mimeType"] != "image/png" ||
		image["sizeBytes"] != len(imageBytes) ||
		image["revisedPrompt"] != "clearer prompt" ||
		!strings.HasPrefix(contracts.AnyStringNode(image["url"]), "/api/resource?file=") {
		t.Fatalf("unexpected image metadata: %#v", image)
	}
	if result.Structured["rawCreated"] != int64(123) {
		t.Fatalf("expected rawCreated, got %#v", result.Structured)
	}
}

func TestImageGenerateURLResponseDoesNotPersistArtifact(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"url":"https://cdn.example/image.png","revised_prompt":"cdn prompt"}]}`))
	}))
	defer modelServer.Close()

	chatsRoot := t.TempDir()
	registry := writeImageGenerateRegistry(t, modelServer.URL, true)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, chatsRoot)
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt":          "draw",
		"response_format": "url",
	}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"},
	})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	images, ok := result.Structured["images"].([]map[string]any)
	if result.Error != "" || !ok || len(images) != 1 {
		t.Fatalf("expected URL image result, got %#v", result)
	}
	if images[0]["url"] != "https://cdn.example/image.png" || images[0]["path"] != nil {
		t.Fatalf("unexpected URL image metadata: %#v", images[0])
	}
	if _, err := os.Stat(filepath.Join(chatsRoot, "chat-1", "artifacts", "run-1")); !os.IsNotExist(err) {
		t.Fatalf("did not expect artifact directory, stat err=%v", err)
	}
}

func TestImageGenerateRejectsEmptyData(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer modelServer.Close()

	registry := writeImageGenerateRegistry(t, modelServer.URL, true)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, t.TempDir())
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "draw",
	}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"},
	})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_model_response_invalid" {
		t.Fatalf("expected invalid response error, got %#v", result)
	}
}

func defaultImageGenerateTestConfig() config.ImageGenerateConfig {
	return config.ImageGenerateConfig{
		Enabled:        true,
		DefaultProfile: "general",
		Profiles: map[string]config.ImageGenerateProfileConfig{
			"general": {
				ModelKey:        "image-model",
				Timeout:         120,
				Size:            "1024x1024",
				ResponseFormat:  "b64_json",
				OutputMimeType:  "image/png",
				MaxPromptChars:  4000,
				PersistArtifact: true,
			},
		},
	}
}

func imageGenerateTestExecutor(cfg config.ImageGenerateConfig, registry *models.ModelRegistry, chatsRoot string) *RuntimeToolExecutor {
	if len(cfg.Profiles) == 0 {
		cfg = defaultImageGenerateTestConfig()
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			ImageGenerate: cfg,
			Paths:         config.PathsConfig{ChatsDir: chatsRoot},
		},
		models: registry,
	}
	executor.httpClient = http.DefaultClient
	return executor
}

func writeImageGenerateRegistry(t *testing.T, baseURL string, withAPIKey bool) *models.ModelRegistry {
	return writeImageGenerateRegistryWithType(t, baseURL, withAPIKey, models.ModelTypeImageGeneration)
}

func writeImageGenerateRegistryWithType(t *testing.T, baseURL string, withAPIKey bool, modelType string) *models.ModelRegistry {
	return writeImageGenerateRegistryWithModel(t, baseURL, withAPIKey, modelType, []string{
		"  endpointPath: /v1/images/generations",
		"  timeout: 120",
		"  defaultSize: 1024x1024",
		"  responseFormats:",
		"    - b64_json",
		"    - url",
	})
}

func writeImageGenerateRegistryWithImageConfig(t *testing.T, baseURL string, withAPIKey bool, imageLines []string) *models.ModelRegistry {
	return writeImageGenerateRegistryWithModel(t, baseURL, withAPIKey, models.ModelTypeImageGeneration, imageLines)
}

func writeImageGenerateRegistryWithModel(t *testing.T, baseURL string, withAPIKey bool, modelType string, imageLines []string) *models.ModelRegistry {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "providers"), 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "models"), 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	providerLines := []string{
		"key: test",
		"baseUrl: " + baseURL,
		"defaultModel: image-model",
		"protocols:",
		"  OPENAI:",
		"    endpointPath: /v1/chat/completions",
	}
	if withAPIKey {
		providerLines = append(providerLines[:2], append([]string{"apiKey: test-key"}, providerLines[2:]...)...)
	}
	if err := os.WriteFile(filepath.Join(root, "providers", "test.yml"), []byte(strings.Join(providerLines, "\n")), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	model := strings.Join([]string{
		"key: image-model",
		"provider: test",
		"type: " + modelType,
		"protocol: OPENAI",
		"modelId: image-model-id",
		"image:",
	}, "\n")
	if len(imageLines) > 0 {
		model += "\n" + strings.Join(imageLines, "\n")
	}
	if err := os.WriteFile(filepath.Join(root, "models", "image-model.yml"), []byte(model), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	return registry
}
