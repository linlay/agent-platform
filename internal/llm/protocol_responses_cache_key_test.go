package llm

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/modelresponses"
	"agent-platform/internal/models"
)

type responsesCacheKeyTransport func(*http.Request) (*http.Response, error)

func (f responsesCacheKeyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResponsesCacheKeyAcrossRetriesTurnsAndRestoredRuns(t *testing.T) {
	chatID := "客户/自定义会话:123"
	var sentKeys []string
	client := &http.Client{Transport: responsesCacheKeyTransport(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		key, _ := body["prompt_cache_key"].(string)
		sentKeys = append(sentKeys, key)
		status, contentType := http.StatusOK, "text/event-stream"
		response := "data: " + `{"type":"response.completed","response":{"id":"resp","status":"completed","output":[{"type":"message","id":"m","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"
		if len(sentKeys) == 1 {
			status, contentType, response = http.StatusServiceUnavailable, "application/json", `{"error":{"code":"server_error","message":"unavailable"}}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})}
	// Recreate stream state as for a later Run / process restart: only the Chat
	// identity is carried forward, never a saved prompt_cache_key.
	for _, runID := range []string{"run-1", "run-after-restart"} {
		s := newRetryTestStream(&retryProtocolStub{}, 1)
		s.session.ChatID, s.session.RunID = chatID, runID
		s.model.Protocol, s.model.ContextWindow = modelresponses.Protocol, 128000
		s.provider.BaseURL, s.provider.APIKey = "https://example.test", "test"
		s.protocolConfig.EndpointPath = "/v1/responses"
		s.engine.httpClient = client
		s.protocol = &responsesProtocol{engine: s.engine}
		if err := s.prepareNextTurn(); err != nil {
			t.Fatal(err)
		}
		prepared := s.modelCall.prepared
		if prepared.RequestBody["prompt_cache_key"] != modelresponses.PromptCacheKey(chatID) {
			t.Fatal("Chat identity was not passed to request preparation")
		}
		for {
			delta, err := s.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if d, ok := delta.(contracts.DeltaLLMRequest); ok {
				data, _ := json.Marshal(d)
				if strings.Contains(string(data), "prompt_cache_key") || strings.Contains(string(data), modelresponses.PromptCacheKey(chatID)) {
					t.Fatal("derived key leaked into JSONL request metadata")
				}
			}
		}
		// A new model call in this Run rebuilds the request after tool/history
		// changes. Its affinity must remain the same even for a summary call.
		s.finished, s.modelCall = false, nil
		s.summaryCall = true
		s.stageSettings.MaxOutputTokens = 100
		s.messages = append(s.messages, openAIMessage{Role: "user", Content: "summarize"})
		if err := s.prepareNextTurn(); err != nil {
			t.Fatal(err)
		}
		if s.modelCall.prepared.RequestBody["prompt_cache_key"] != prepared.RequestBody["prompt_cache_key"] {
			t.Fatal("summary/rebuilt request changed affinity")
		}
	}
	if want := []string{modelresponses.PromptCacheKey(chatID), modelresponses.PromptCacheKey(chatID), modelresponses.PromptCacheKey(chatID)}; !reflect.DeepEqual(sentKeys, want) {
		t.Fatalf("retry/restored request keys=%v", sentKeys)
	}
}

func TestResponsesCacheKeyOverridesCompatAndStaysOutOfMetadata(t *testing.T) {
	p := &responsesProtocol{}
	params := protocolStreamParams{chatID: "chat-a", provider: models.ProviderDefinition{BaseURL: "https://example.test", APIKey: "test"}, model: models.ModelDefinition{Protocol: modelresponses.Protocol}, protocolConfig: protocolRuntimeConfig{EndpointPath: "/v1/responses", Compat: map[string]any{"request": map[string]any{"always": map[string]any{"prompt_cache_key": "global-key"}}}}}
	for _, chatID := range []string{"chat-a", "chat-b", ""} {
		params.chatID = chatID
		req, err := p.PrepareRequest(params)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err = json.Unmarshal(req.RequestBodyJSON, &wire); err != nil {
			t.Fatal(err)
		}
		if chatID == "" {
			if _, ok := wire["prompt_cache_key"]; ok {
				t.Fatal("chatless call has key")
			}
		} else if wire["prompt_cache_key"] != modelresponses.PromptCacheKey(chatID) {
			t.Fatal("wrong outgoing key")
		}
		if _, ok := requestOptionsFromPreparedBody(req.RequestBody)["prompt_cache_key"]; ok {
			t.Fatal("key would be saved in system-init or react metadata")
		}
	}
	params.protocolConfig.Compat = nil
	params.chatID = "chat-a"
	legacy, err := (&openAIProtocol{}).PrepareRequest(params)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := legacy.RequestBody["prompt_cache_key"]; ok {
		t.Fatal("Responses affinity leaked into Chat Completions")
	}
}
