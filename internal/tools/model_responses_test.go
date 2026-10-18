package tools

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/modelrequest"
	"agent-platform/internal/models"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponsesAuxiliaryTextAndImage(t *testing.T) {
	affinity := modelrequest.AffinityKey("chat-1", "run-1")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path=%s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["store"] != false || body["stream"] != false || body["messages"] != nil || body["max_output_tokens"] != float64(100) {
			t.Errorf("bad body %v", body)
		}
		input := body["input"].([]any)
		if body["prompt_cache_key"] != affinity {
			t.Errorf("auxiliary call key = %v, want the calling Chat's key", body["prompt_cache_key"])
		}
		if calls == 2 {
			content := input[0].(map[string]any)["content"].([]any)
			if content[1].(map[string]any)["type"] != "input_image" {
				t.Error(content)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":10}}`)
	}))
	defer server.Close()
	ex := &RuntimeToolExecutor{httpClient: server.Client()}
	model := models.ModelDefinition{Key: "test", Protocol: "OPENAI_RESPONSES", ModelID: "model"}
	provider := models.ProviderDefinition{BaseURL: server.URL}
	answer, _, err := ex.completeTextModel(context.Background(), model, provider, textModelRequest{SystemPrompt: "rules", UserPrompt: "question", MaxOutputTokens: 100, AffinityKey: affinity})
	if err != nil || answer != "answer" {
		t.Fatalf("%s %v", answer, err)
	}
	_, _, err = ex.completeResponsesModel(context.Background(), model, provider, []contracts.ModelMessage{{Role: "user", Content: []any{map[string]any{"type": "text", "text": "describe"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}}}}, 100, affinity)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuxiliaryAnthropicCallSendsAffinityMetadata(t *testing.T) {
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"}}
	var metadata any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		metadata = body["metadata"]
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn"}`)
	}))
	defer server.Close()
	ex := &RuntimeToolExecutor{httpClient: server.Client()}
	model := models.ModelDefinition{Key: "test", Protocol: "ANTHROPIC", ModelID: "model"}
	provider := models.ProviderDefinition{BaseURL: server.URL}
	if _, _, err := ex.completeTextModel(context.Background(), model, provider, textModelRequest{UserPrompt: "q", AffinityKey: toolCallAffinityKey(execCtx)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := metadata.(map[string]any); got["user_id"] != modelrequest.AffinityKey("chat-1", "run-1") {
		t.Fatalf("metadata = %#v", metadata)
	}
}
