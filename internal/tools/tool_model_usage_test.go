package tools

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/modelusage"
)

func TestImageGenerateRecordsProviderUsage(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("fake image bytes"))
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + encoded + `"}],"usage":{"input_tokens":12,"output_tokens":1056,"total_tokens":1068,"input_tokens_details":{"cached_tokens":2}}}`))
	}))
	defer modelServer.Close()

	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), writeImageGenerateRegistry(t, modelServer.URL, true), t.TempDir())
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{"prompt": "draw"}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"},
	})
	if err != nil || result.Error != "" {
		t.Fatalf("invokeImageGenerate result=%#v err=%v", result, err)
	}
	want := contracts.ModelUsage{
		ModelKey: "image-model", Operation: modelusage.OperationImageGeneration,
		PromptTokens: 12, CompletionTokens: 1056, TotalTokens: 1068, CacheHitTokens: 2, CacheMissTokens: 10,
	}
	if len(result.ModelUsage) != 1 || result.ModelUsage[0] != want {
		t.Fatalf("unexpected model usage %#v", result.ModelUsage)
	}
}

func TestImageGenerateWithoutProviderUsageRecordsNothing(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("fake image bytes"))
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + encoded + `"}]}`))
	}))
	defer modelServer.Close()

	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), writeImageGenerateRegistry(t, modelServer.URL, true), t.TempDir())
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{"prompt": "draw"}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"},
	})
	if err != nil || result.Error != "" {
		t.Fatalf("invokeImageGenerate result=%#v err=%v", result, err)
	}
	if len(result.ModelUsage) != 0 {
		t.Fatalf("missing provider usage must count as zero, got %#v", result.ModelUsage)
	}
}

func TestImageGenerateKeepsUsageWhenMaterializationFails(t *testing.T) {
	var modelServer *httptest.Server
	modelServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/image.png" {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"url":"` + modelServer.URL + `/image.png"}],"usage":{"input_tokens":5,"output_tokens":100,"total_tokens":105}}`))
	}))
	defer modelServer.Close()

	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), writeImageGenerateRegistry(t, modelServer.URL, true), t.TempDir())
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{"prompt": "draw", "responseFormat": "url"}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"},
	})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_model_response_invalid" {
		t.Fatalf("expected materialization failure, got %#v", result)
	}
	if len(result.ModelUsage) != 1 || result.ModelUsage[0].TotalTokens != 105 || result.ModelUsage[0].ModelKey != "image-model" {
		t.Fatalf("billed usage must survive materialization failure, got %#v", result.ModelUsage)
	}
}

func TestImageGenerateRequestFailureRecordsNoUsage(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
	}))
	defer modelServer.Close()

	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), writeImageGenerateRegistry(t, modelServer.URL, true), t.TempDir())
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{"prompt": "draw"}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"},
	})
	if err != nil || result.Error != "image_generate_model_request_failed" {
		t.Fatalf("expected request failure, got %#v err=%v", result, err)
	}
	if len(result.ModelUsage) != 0 {
		t.Fatalf("failed request has no provider usage, got %#v", result.ModelUsage)
	}
}

func TestVisionRecognizeRecordsProviderUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"a tiny image"}}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`))
	}))
	defer server.Close()

	chatsDir := t.TempDir()
	writeTestPNG(t, filepath.Join(chatsDir, "chat-1", "demo.png"))
	executor := visionTestExecutor(chatsDir, writeVisionRegistry(t, server.URL, "OPENAI", models.ModelTypeVL), server.Client())
	result, err := executor.invokeVisionRecognize(context.Background(), map[string]any{
		"images": []any{map[string]any{"referenceName": "demo.png"}},
		"prompt": "describe this",
	}, &contracts.ExecutionContext{Request: apiQuery("chat-1", "demo.png")})
	if err != nil || result.Error != "" {
		t.Fatalf("invokeVisionRecognize result=%#v err=%v", result, err)
	}
	want := contracts.ModelUsage{
		ModelKey: "vision-model", Operation: modelusage.OperationVision,
		PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13,
	}
	if len(result.ModelUsage) != 1 || result.ModelUsage[0] != want {
		t.Fatalf("unexpected model usage %#v", result.ModelUsage)
	}
}
