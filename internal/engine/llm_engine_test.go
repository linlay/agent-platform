package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
)

func TestLLMAgentEngineStreamsContentDeltas(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"hello "}}]}`,
			`{"choices":[{"delta":{"content":"world"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}))
	defer modelServer.Close()

	engine := NewLLMAgentEngine(
		config.Config{Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}}},
		newTestModelRegistry(modelServer.URL),
		&testToolExecutor{},
		NewNoopSandboxClient(),
	)
	stream, err := engine.Stream(context.Background(), api.QueryRequest{Message: "hi"}, QuerySession{
		RunID:    "run_1",
		ChatID:   "chat_1",
		ModelKey: "mock-model",
	})
	if err != nil {
		t.Fatalf("stream query: %v", err)
	}
	defer stream.Close()

	first, err := stream.Next()
	if err != nil {
		t.Fatalf("first next: %v", err)
	}
	if got, _ := first["type"].(string); got != "content.delta" {
		t.Fatalf("expected content.delta, got %#v", first)
	}
	if got, _ := first["delta"].(string); got != "hello " {
		t.Fatalf("expected first streamed delta, got %#v", first)
	}

	second, err := stream.Next()
	if err != nil {
		t.Fatalf("second next: %v", err)
	}
	if got, _ := second["delta"].(string); got != "world" {
		t.Fatalf("expected second streamed delta, got %#v", second)
	}

	if _, err := stream.Next(); err != io.EOF {
		t.Fatalf("expected eof, got %v", err)
	}
}

func TestLLMAgentEngineAccumulatesToolCallFragments(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		messages, _ := request["messages"].([]any)
		hasToolMessage := false
		for _, raw := range messages {
			msg, _ := raw.(map[string]any)
			if role, _ := msg["role"].(string); role == "tool" {
				hasToolMessage = true
				break
			}
		}
		if !hasToolMessage {
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_math","type":"function","function":{"name":"mock.tool","arguments":"{"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"value\":1}"}}],"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
			return
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}))
	defer modelServer.Close()

	tools := &testToolExecutor{
		definitions: []api.ToolDetailResponse{
			{
				Key:  "mock.tool",
				Name: "mock.tool",
				Parameters: map[string]any{
					"type": "object",
				},
			},
		},
		result: ToolExecutionResult{
			Output: "ok",
		},
	}
	engine := NewLLMAgentEngine(
		config.Config{
			SSE:      config.SSEConfig{IncludeToolPayloadEvents: true},
			Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}},
		},
		newTestModelRegistry(modelServer.URL),
		tools,
		NewNoopSandboxClient(),
	)
	stream, err := engine.Stream(context.Background(), api.QueryRequest{Message: "call tool"}, QuerySession{
		RunID:     "run_tool",
		ChatID:    "chat_tool",
		ModelKey:  "mock-model",
		ToolNames: []string{"mock.tool"},
	})
	if err != nil {
		t.Fatalf("stream query: %v", err)
	}
	defer stream.Close()

	var seenTypes []string
	for {
		event, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if eventType, _ := event["type"].(string); eventType != "" {
			seenTypes = append(seenTypes, eventType)
		}
	}

	if len(tools.invocations) != 1 {
		t.Fatalf("expected one tool invocation, got %#v", tools.invocations)
	}
	if got := tools.invocations[0]["value"]; got != float64(1) {
		t.Fatalf("expected accumulated tool arguments, got %#v", tools.invocations[0])
	}
	assertContainsType(t, seenTypes, "tool.start")
	assertContainsType(t, seenTypes, "tool.snapshot")
	assertContainsType(t, seenTypes, "tool.result")
	assertContainsType(t, seenTypes, "content.delta")
}

type testToolExecutor struct {
	definitions []api.ToolDetailResponse
	result      ToolExecutionResult
	invocations []map[string]any
}

func (t *testToolExecutor) Definitions() []api.ToolDetailResponse {
	return t.definitions
}

func (t *testToolExecutor) Invoke(_ context.Context, _ string, args map[string]any, _ *ExecutionContext) (ToolExecutionResult, error) {
	t.invocations = append(t.invocations, args)
	if t.result.Output == "" && len(t.result.Structured) == 0 && t.result.Error == "" {
		return ToolExecutionResult{Output: "ok"}, nil
	}
	return t.result, nil
}

func newTestModelRegistry(baseURL string) *ModelRegistry {
	return &ModelRegistry{
		providers: map[string]ProviderDefinition{
			"mock": {
				Key:          "mock",
				BaseURL:      baseURL,
				APIKey:       "test-key",
				DefaultModel: "mock-model",
				EndpointPath: "/v1/chat/completions",
			},
		},
		models: map[string]ModelDefinition{
			"mock-model": {
				Key:      "mock-model",
				Provider: "mock",
				Protocol: "OPENAI",
				ModelID:  "mock-model",
			},
		},
	}
}

func writeProviderSSE(t *testing.T, w http.ResponseWriter, frames ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatalf("expected flusher")
	}
	for _, frame := range frames {
		if _, err := io.WriteString(w, "data: "+frame+"\n\n"); err != nil {
			t.Fatalf("write sse frame: %v", err)
		}
		flusher.Flush()
	}
}

func assertContainsType(t *testing.T, seen []string, want string) {
	t.Helper()
	for _, value := range seen {
		if value == want {
			return
		}
	}
	t.Fatalf("expected event type %s in %#v", want, seen)
}
