package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestVisionRecognizeDisabled(t *testing.T) {
	executor := &RuntimeToolExecutor{cfg: config.Config{}}
	result, err := executor.invokeVisionRecognize(context.Background(), map[string]any{}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeVisionRecognize: %v", err)
	}
	if result.Error != "vision_recognize_disabled" {
		t.Fatalf("expected disabled error, got %#v", result)
	}
}

func TestVisionRecognizeRejectsNonVLModel(t *testing.T) {
	registry := writeVisionRegistry(t, "http://127.0.0.1:1", "OPENAI", models.ModelTypeChat)
	executor := &RuntimeToolExecutor{
		cfg: config.Config{VisionRecognize: config.VisionRecognizeConfig{
			Enabled:        true,
			DefaultProfile: "general",
			Profiles: map[string]config.VisionRecognizeProfileConfig{
				"general": {ModelKey: "vision-model"},
			},
		}},
		models: registry,
	}
	result, err := executor.invokeVisionRecognize(context.Background(), map[string]any{
		"images": []any{map[string]any{"reference_name": "demo.png"}},
		"prompt": "describe",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeVisionRecognize: %v", err)
	}
	if result.Error != "vision_model_not_vl" {
		t.Fatalf("expected non-vl model error, got %#v", result)
	}
}

func TestVisionRecognizeImagesArrayContract(t *testing.T) {
	executor := &RuntimeToolExecutor{}
	profile := config.VisionRecognizeProfileConfig{}
	tests := []struct {
		name       string
		args       map[string]any
		wantCode   string
		actualType string
	}{
		{name: "missing", args: map[string]any{}, wantCode: "vision_images_required"},
		{name: "null", args: map[string]any{"images": nil}, wantCode: "vision_images_required"},
		{name: "empty", args: map[string]any{"images": []any{}}, wantCode: "vision_images_required"},
		{name: "single object", args: map[string]any{"images": map[string]any{"file_path": "@chat/image.png"}}, wantCode: "vision_images_invalid_type", actualType: "object"},
		{name: "string", args: map[string]any{"images": "@chat/image.png"}, wantCode: "vision_images_invalid_type", actualType: "string"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, result, handled := executor.loadVisionImages(test.args, &contracts.ExecutionContext{}, profile)
			if !handled || result.Error != test.wantCode {
				t.Fatalf("handled=%t result=%#v, want %s", handled, result, test.wantCode)
			}
			if test.actualType == "" {
				return
			}
			if result.Structured["expectedType"] != "array" || result.Structured["actualType"] != test.actualType {
				t.Fatalf("unexpected diagnostics %#v", result.Structured)
			}
			message, _ := result.Structured["message"].(string)
			if !strings.Contains(message, `"images":[{"file_path":"@chat/image.png"}]`) {
				t.Fatalf("missing correction example: %s", message)
			}
		})
	}
}

func TestVisionRecognizeAcceptsMultipleImages(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"two images"}}]}`))
	}))
	defer server.Close()

	chatsDir := t.TempDir()
	chatID := "chat-multiple"
	writeTestPNG(t, filepath.Join(chatsDir, chatID, "first.png"))
	writeTestPNG(t, filepath.Join(chatsDir, chatID, "second.png"))
	registry := writeVisionRegistry(t, server.URL, "OPENAI", models.ModelTypeVL)
	executor := visionTestExecutor(chatsDir, registry, server.Client())
	result, err := executor.invokeVisionRecognize(context.Background(), map[string]any{
		"images": []any{
			map[string]any{"reference_name": "first.png"},
			map[string]any{"reference_name": "second.png"},
		},
		"prompt": "compare",
	}, &contracts.ExecutionContext{Request: api.QueryRequest{
		ChatID: chatID,
		References: []api.Reference{
			{Name: "first.png", MimeType: "image/png"},
			{Name: "second.png", MimeType: "image/png"},
		},
	}})
	if err != nil || result.Error != "" {
		t.Fatalf("invoke result=%#v err=%v", result, err)
	}
	messages, _ := captured["messages"].([]any)
	user, _ := messages[1].(map[string]any)
	content, _ := user["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("expected prompt plus two images, got %#v", user["content"])
	}
}

func TestVisionRecognizeReferenceNameOpenAI(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected authorization: %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"a tiny image"}}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`))
	}))
	defer server.Close()

	chatsDir := t.TempDir()
	chatID := "chat-1"
	writeTestPNG(t, filepath.Join(chatsDir, chatID, "demo.png"))
	registry := writeVisionRegistry(t, server.URL, "OPENAI", models.ModelTypeVL)
	executor := visionTestExecutor(chatsDir, registry, server.Client())

	result, err := executor.invokeVisionRecognize(context.Background(), map[string]any{
		"images": []any{map[string]any{"reference_name": "demo.png"}},
		"prompt": "describe this",
	}, &contracts.ExecutionContext{Request: apiQuery("chat-1", "demo.png")})
	if err != nil {
		t.Fatalf("invokeVisionRecognize: %v", err)
	}
	if result.Error != "" || result.Structured["content"] != "a tiny image" {
		t.Fatalf("expected success, got %#v", result)
	}
	messages, _ := captured["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected two messages, got %#v", captured["messages"])
	}
	userMessage, _ := messages[1].(map[string]any)
	content, _ := userMessage["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected text + image content, got %#v", userMessage["content"])
	}
	imageBlock, _ := content[1].(map[string]any)
	imageURL, _ := imageBlock["image_url"].(map[string]any)
	if !strings.HasPrefix(strings.TrimSpace(imageURL["url"].(string)), "data:image/png;base64,") {
		t.Fatalf("expected data image url, got %#v", imageURL)
	}
}

func TestVisionRecognizeAnthropicRequest(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Fatalf("unexpected api key: %q", r.Header.Get("X-Api-Key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"anthropic vision"}],"usage":{"input_tokens":8,"output_tokens":2}}`))
	}))
	defer server.Close()

	chatsDir := t.TempDir()
	chatID := "chat-1"
	writeTestPNG(t, filepath.Join(chatsDir, chatID, "demo.png"))
	registry := writeVisionRegistry(t, server.URL, "ANTHROPIC", models.ModelTypeVL)
	executor := visionTestExecutor(chatsDir, registry, server.Client())

	result, err := executor.invokeVisionRecognize(context.Background(), map[string]any{
		"images":        []any{map[string]any{"reference_name": "demo.png"}},
		"prompt":        "extract text",
		"output_format": "json",
	}, &contracts.ExecutionContext{Request: apiQuery("chat-1", "demo.png")})
	if err != nil {
		t.Fatalf("invokeVisionRecognize: %v", err)
	}
	if result.Error != "" || result.Structured["content"] != "anthropic vision" {
		t.Fatalf("expected success, got %#v", result)
	}
	messages, _ := captured["messages"].([]any)
	userMessage, _ := messages[0].(map[string]any)
	content, _ := userMessage["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected text + image content, got %#v", userMessage["content"])
	}
	imageBlock, _ := content[1].(map[string]any)
	source, _ := imageBlock["source"].(map[string]any)
	if source["media_type"] != "image/png" || strings.TrimSpace(source["data"].(string)) == "" {
		t.Fatalf("unexpected anthropic image source: %#v", source)
	}
}

func TestVisionRecognizeFilePathRequiresApprovalOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	imagePath := filepath.Join(outside, "demo.png")
	writeTestPNG(t, imagePath)
	registry := writeVisionRegistry(t, "http://127.0.0.1:1", "OPENAI", models.ModelTypeVL)
	executor := visionTestExecutor(t.TempDir(), registry, nil)

	result, err := executor.invokeVisionRecognize(context.Background(), map[string]any{
		"images": []any{map[string]any{"file_path": imagePath}},
		"prompt": "describe",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{WorkspaceRoot: root}})
	if err != nil {
		t.Fatalf("invokeVisionRecognize: %v", err)
	}
	if result.Error != "vision_recognize_approval_required" || result.Structured["fingerprint"] == "" {
		t.Fatalf("expected approval requirement, got %#v", result)
	}
}

func visionTestExecutor(chatsDir string, registry *models.ModelRegistry, client *http.Client) *RuntimeToolExecutor {
	cfg := config.Config{
		Paths: config.PathsConfig{ChatsDir: chatsDir},
		VisionRecognize: config.VisionRecognizeConfig{
			Enabled:        true,
			DefaultProfile: "general",
			Profiles: map[string]config.VisionRecognizeProfileConfig{
				"general": {
					ModelKey:      "vision-model",
					Timeout:       60,
					MaxImages:     4,
					MaxImageBytes: 20971520,
					OutputFormat:  "text",
					SystemPrompt:  "Recognize images.",
				},
			},
		},
	}
	executor := &RuntimeToolExecutor{cfg: cfg, models: registry}
	if client != nil {
		executor.httpClient = client
	}
	return executor
}

func writeVisionRegistry(t *testing.T, baseURL string, protocol string, modelType string) *models.ModelRegistry {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "providers"), 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "models"), 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	provider := strings.Join([]string{
		"key: test",
		"baseUrl: " + baseURL,
		"apiKey: test-key",
		"defaultModel: vision-model",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "providers", "test.yml"), []byte(provider), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	model := strings.Join([]string{
		"key: vision-model",
		"provider: test",
		"type: " + modelType,
		"protocol: " + protocol,
		"modelId: vision-model-id",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "models", "vision-model.yml"), []byte(model), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	return registry
}

func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

func apiQuery(chatID string, name string) api.QueryRequest {
	return api.QueryRequest{
		ChatID: chatID,
		References: []api.Reference{{
			Name:     name,
			MimeType: "image/png",
		}},
	}
}
