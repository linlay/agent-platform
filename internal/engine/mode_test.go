package engine

import (
	"context"
	"io"
	"net/http"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
)

func TestOneshotModeRejectsToolCalls(t *testing.T) {
	client := newScriptedHTTPClient(func(*http.Request) scriptedHTTPResponse {
		return scriptedSSE(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"mock.tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			`[DONE]`,
		)
	})
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}}},
		newTestModelRegistry("http://mock.local"),
		&testToolExecutor{definitions: []api.ToolDetailResponse{{Key: "mock.tool", Name: "mock.tool"}}},
		NewNoopSandboxClient(),
		client,
	)

	stream, err := engine.Stream(context.Background(), api.QueryRequest{Message: "hi"}, QuerySession{
		RunID:     "run_oneshot",
		ChatID:    "chat_oneshot",
		ModelKey:  "mock-model",
		Mode:      "ONESHOT",
		ToolNames: []string{"mock.tool"},
	})
	if err != nil {
		t.Fatalf("stream query: %v", err)
	}
	defer stream.Close()

	event, err := stream.Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	deltaErr, ok := event.(DeltaError)
	if !ok {
		t.Fatalf("expected DeltaError, got %#v", event)
	}
	if deltaErr.Error["code"] != "tool_calls_not_allowed" {
		t.Fatalf("unexpected error payload: %#v", deltaErr.Error)
	}
	if _, err := stream.Next(); err != io.EOF {
		t.Fatalf("expected eof, got %v", err)
	}
}

func TestPlanExecuteModePrependsPlanLifecycleEvents(t *testing.T) {
	client := newScriptedHTTPClient(func(*http.Request) scriptedHTTPResponse {
		return scriptedSSE(
			`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}}},
		newTestModelRegistry("http://mock.local"),
		&testToolExecutor{},
		NewNoopSandboxClient(),
		client,
	)

	stream, err := engine.Stream(context.Background(), api.QueryRequest{Message: "plan this"}, QuerySession{
		RunID:    "run_plan",
		ChatID:   "chat_plan",
		ModelKey: "mock-model",
		Mode:     "PLAN_EXECUTE",
	})
	if err != nil {
		t.Fatalf("stream query: %v", err)
	}
	defer stream.Close()

	first, err := stream.Next()
	if err != nil {
		t.Fatalf("first next: %v", err)
	}
	if _, ok := first.(DeltaPlanUpdate); !ok {
		t.Fatalf("expected first event to be DeltaPlanUpdate, got %#v", first)
	}
	second, err := stream.Next()
	if err != nil {
		t.Fatalf("second next: %v", err)
	}
	task, ok := second.(DeltaTaskLifecycle)
	if !ok || task.Kind != "start" {
		t.Fatalf("expected task start after plan update, got %#v", second)
	}
}
