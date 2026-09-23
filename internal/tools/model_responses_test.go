package tools

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponsesAuxiliaryTextAndImage(t *testing.T) {
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
	answer, _, err := ex.completeTextModel(context.Background(), model, provider, textModelRequest{SystemPrompt: "rules", UserPrompt: "question", MaxOutputTokens: 100})
	if err != nil || answer != "answer" {
		t.Fatalf("%s %v", answer, err)
	}
	_, _, err = ex.completeResponsesModel(context.Background(), model, provider, []contracts.ModelMessage{{Role: "user", Content: []any{map[string]any{"type": "text", "text": "describe"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}}}}, 100)
	if err != nil {
		t.Fatal(err)
	}
}
