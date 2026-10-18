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
	"agent-platform/internal/modelrequest"
	"agent-platform/internal/models"
)

const anthropicAffinityTestSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":90,\"output_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestAnthropicAffinityMetadataAcrossRetriesTurnsAndRestoredRuns(t *testing.T) {
	chatID := "客户/自定义会话:123"
	var sentKeys []string
	client := &http.Client{Transport: responsesCacheKeyTransport(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		metadata, _ := body["metadata"].(map[string]any)
		key, _ := metadata["user_id"].(string)
		sentKeys = append(sentKeys, key)
		status, contentType, response := http.StatusOK, "text/event-stream", anthropicAffinityTestSSE
		if len(sentKeys) == 1 {
			status, contentType, response = http.StatusServiceUnavailable, "application/json", `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`
		}
		header := http.Header{"Content-Type": {contentType}, "X-Babelark-Request-Id": {"relay-" + string(rune('0'+len(sentKeys)))}}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})}
	var upstreamIDs []string
	for _, runID := range []string{"run-1", "run-after-restart"} {
		s := newRetryTestStream(&retryProtocolStub{}, 1)
		s.session.ChatID, s.session.RunID = chatID, runID
		s.model.Protocol, s.model.ContextWindow = "ANTHROPIC", 128000
		s.provider.BaseURL, s.provider.APIKey = "https://example.test", "test"
		s.provider.RequestIDHeader = "X-Babelark-Request-Id"
		s.protocolConfig = resolveProtocolRuntimeConfig(s.provider, s.model)
		s.protocolConfig.EndpointPath = "/v1/messages"
		s.engine.httpClient = client
		s.protocol = &anthropicProtocol{engine: s.engine}
		if err := s.prepareNextTurn(); err != nil {
			t.Fatal(err)
		}
		metadata, _ := s.modelCall.prepared.RequestBody["metadata"].(map[string]any)
		if metadata["user_id"] != modelrequest.AffinityKey(chatID, "") {
			t.Fatalf("metadata = %#v", s.modelCall.prepared.RequestBody["metadata"])
		}
		for {
			delta, err := s.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			switch d := delta.(type) {
			case contracts.DeltaLLMRequest:
				data, _ := json.Marshal(d)
				if strings.Contains(string(data), modelrequest.AffinityKey(chatID, "")) {
					t.Fatal("derived key leaked into JSONL request metadata")
				}
			case contracts.DeltaUsageSnapshot:
				upstreamIDs = append(upstreamIDs, d.LLMReturnUpstreamRequestID)
			}
		}
	}
	key := modelrequest.AffinityKey(chatID, "")
	if want := []string{key, key, key}; !reflect.DeepEqual(sentKeys, want) {
		t.Fatalf("retry/restored request keys=%v", sentKeys)
	}
	// Usage reports the successful attempt's relay ID, never the failed one.
	if want := []string{"relay-2", "relay-3"}; !reflect.DeepEqual(upstreamIDs, want) {
		t.Fatalf("usage upstream request IDs=%v", upstreamIDs)
	}
}

func TestAnthropicAffinityMetadataOverridesCompatAndFallsBackToRun(t *testing.T) {
	p := &anthropicProtocol{}
	params := protocolStreamParams{runID: "run-a", provider: models.ProviderDefinition{BaseURL: "https://example.test", APIKey: "test"}, model: models.ModelDefinition{Protocol: "ANTHROPIC", ModelID: "m"},
		protocolConfig: protocolRuntimeConfig{EndpointPath: "/v1/messages", Compat: map[string]any{"request": map[string]any{"always": map[string]any{"metadata": map[string]any{"user_id": "static"}}}}},
		messages:       []openAIMessage{{Role: "user", Content: "hi"}}}
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
		metadata, _ := wire["metadata"].(map[string]any)
		if want := modelrequest.AffinityKey(chatID, params.runID); want == "" || len(metadata) != 1 || metadata["user_id"] != want {
			t.Fatalf("chat %q metadata=%#v", chatID, wire["metadata"])
		}
		if _, ok := requestOptionsFromPreparedBody(req.RequestBody)["metadata"]; ok {
			t.Fatal("affinity metadata would be saved in system-init or react metadata")
		}
	}
}
