package engine

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
)

func TestOneshotModeAllowsToolCalls(t *testing.T) {
	var mu sync.Mutex
	callCount := 0
	client := newScriptedHTTPClient(func(*http.Request) scriptedHTTPResponse {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		if callCount == 1 {
			return scriptedSSE(
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"mock.tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		}
		return scriptedSSE(
			`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
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

	// ONESHOT now allows tool use (Java behaviour), should get tool events then content
	sawToolResult := false
	sawContent := false
	for {
		event, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		switch event.(type) {
		case DeltaToolResult:
			sawToolResult = true
		case DeltaContent:
			sawContent = true
		}
	}
	if !sawToolResult {
		t.Fatal("expected tool result in ONESHOT mode")
	}
	if !sawContent {
		t.Fatal("expected content after tool execution in ONESHOT mode")
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
	if marker, ok := first.(DeltaStageMarker); !ok || marker.Stage != "plan" {
		t.Fatalf("expected first event to be DeltaStageMarker(plan), got %#v", first)
	}
	second, err := stream.Next()
	if err != nil {
		t.Fatalf("second next: %v", err)
	}
	if _, ok := second.(DeltaPlanUpdate); !ok {
		t.Fatalf("expected second event to be DeltaPlanUpdate, got %#v", second)
	}
}

func TestPlanExecuteModeRunsPlanTaskAndSummaryStages(t *testing.T) {
	var mu sync.Mutex
	callCount := 0
	client := newScriptedHTTPClient(func(*http.Request) scriptedHTTPResponse {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		switch callCount {
		case 1:
			return scriptedSSE(
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"plan_add","type":"function","function":{"name":"_plan_add_tasks_","arguments":"{\"tasks\":[{\"taskId\":\"task_1\",\"description\":\"Investigate issue\"}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			return scriptedSSE(
				`{"choices":[{"delta":{"content":"plan ready"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		case 3:
			// Execute: _plan_update_task_ sets task_1 to completed → PostToolHook stops stream
			return scriptedSSE(
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"task_update","type":"function","function":{"name":"_plan_update_task_","arguments":"{\"taskId\":\"task_1\",\"status\":\"completed\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		default:
			// Summary stage
			return scriptedSSE(
				`{"choices":[{"delta":{"content":"final summary"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		}
	})

	tools := NewRuntimeToolExecutor(config.Config{}, NewNoopSandboxClient(), nil)
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{Defaults: config.DefaultsConfig{Plan: config.PlanExecuteDefaultsConfig{MaxSteps: 4, MaxWorkRoundsPerTask: 2}}},
		newTestModelRegistry("http://mock.local"),
		tools,
		NewNoopSandboxClient(),
		client,
	)

	stream, err := engine.Stream(context.Background(), api.QueryRequest{Message: "plan this"}, QuerySession{
		RunID:                 "run_plan_flow",
		ChatID:                "chat_plan_flow",
		ModelKey:              "mock-model",
		Mode:                  "PLAN_EXECUTE",
		ResolvedBudget:        normalizeBudget(Budget{}),
		ResolvedStageSettings: ResolvePlanExecuteSettings(nil, 4, 2),
	})
	if err != nil {
		t.Fatalf("stream query: %v", err)
	}
	defer stream.Close()

	var (
		sawExecuteStage bool
		sawSummaryStage bool
		sawTaskStart    bool
		sawTaskComplete bool
		lastContent     string
	)
	for {
		event, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		switch value := event.(type) {
		case DeltaStageMarker:
			if strings.HasPrefix(value.Stage, "execute") {
				sawExecuteStage = true
			}
			if value.Stage == "summary" {
				sawSummaryStage = true
			}
		case DeltaTaskLifecycle:
			if value.Kind == "start" {
				sawTaskStart = true
			}
			if value.Kind == "complete" {
				sawTaskComplete = true
			}
		case DeltaContent:
			lastContent += value.Text
		}
	}
	if !sawExecuteStage || !sawSummaryStage {
		t.Fatalf("expected execute and summary stages, got execute=%v summary=%v", sawExecuteStage, sawSummaryStage)
	}
	if !sawTaskStart || !sawTaskComplete {
		t.Fatalf("expected task lifecycle events, got start=%v complete=%v", sawTaskStart, sawTaskComplete)
	}
	if !strings.Contains(lastContent, "final summary") {
		t.Fatalf("expected final summary content, got %q", lastContent)
	}
}
