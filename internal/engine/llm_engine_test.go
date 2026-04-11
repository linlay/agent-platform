package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/memory"
)

func TestLLMAgentEngineStreamsContentDeltas(t *testing.T) {
	client := newScriptedHTTPClient(
		func(*http.Request) scriptedHTTPResponse {
			return scriptedSSE(`{"choices":[{"delta":{"content":"hello "}}]}`, `{"choices":[{"delta":{"content":"world"},"finish_reason":"stop"}]}`, `[DONE]`)
		},
	)
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}}},
		newTestModelRegistry("http://mock.local"),
		&testToolExecutor{},
		NewNoopSandboxClient(),
		client,
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
	// Skip react-step marker if present
	if _, isMarker := first.(DeltaStageMarker); isMarker {
		first, err = stream.Next()
		if err != nil {
			t.Fatalf("next after marker: %v", err)
		}
	}
	firstContent, ok := first.(DeltaContent)
	if !ok {
		t.Fatalf("expected DeltaContent, got %#v", first)
	}
	if got := firstContent.Text; got != "hello " {
		t.Fatalf("expected first streamed delta, got %#v", firstContent)
	}

	second, err := stream.Next()
	if err != nil {
		t.Fatalf("second next: %v", err)
	}
	secondContent, ok := second.(DeltaContent)
	if !ok {
		t.Fatalf("expected DeltaContent, got %#v", second)
	}
	if got := secondContent.Text; got != "world" {
		t.Fatalf("expected second streamed delta, got %#v", secondContent)
	}

	third, err := stream.Next()
	if err != nil {
		t.Fatalf("third next: %v", err)
	}
	if finish, ok := third.(DeltaFinishReason); !ok || finish.Reason != "stop" {
		t.Fatalf("expected DeltaFinishReason(stop), got %#v", third)
	}

	if _, err := stream.Next(); err != io.EOF {
		t.Fatalf("expected eof, got %v", err)
	}
}

func TestLLMAgentEngineAccumulatesToolCallFragments(t *testing.T) {
	var mu sync.Mutex
	callCount := 0
	client := newScriptedHTTPClient(func(req *http.Request) scriptedHTTPResponse {
		mu.Lock()
		defer mu.Unlock()

		callCount++
		if callCount == 1 {
			return scriptedSSE(
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_math","type":"function","function":{"name":"mock.tool","arguments":"{"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"value\":1}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		}
		var request map[string]any
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
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
			t.Fatalf("expected second request to include tool message, got %#v", request)
		}
		return scriptedSSE(
			`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})

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
		result: ToolExecutionResult{Output: "ok"},
	}
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{
			SSE:      config.SSEConfig{IncludeToolPayloadEvents: true},
			Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}},
		},
		newTestModelRegistry("http://mock.local"),
		tools,
		NewNoopSandboxClient(),
		client,
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
		seenTypes = append(seenTypes, deltaTypeName(event))
	}

	if len(tools.invocations) != 1 {
		t.Fatalf("expected one tool invocation, got %#v", tools.invocations)
	}
	if got := tools.invocations[0]["value"]; got != float64(1) {
		t.Fatalf("expected accumulated tool arguments, got %#v", tools.invocations[0])
	}
	assertContainsType(t, seenTypes, "tool.args")
	assertContainsType(t, seenTypes, "tool.end")
	assertContainsType(t, seenTypes, "tool.result")
	assertContainsType(t, seenTypes, "content.delta")
	assertContainsType(t, seenTypes, "run.complete")
}

func TestLLMAgentEngineWaitsForToolCallIDBeforeStreamingToolArgs(t *testing.T) {
	var mu sync.Mutex
	callCount := 0
	client := newScriptedHTTPClient(func(*http.Request) scriptedHTTPResponse {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		if callCount == 1 {
			return scriptedSSE(
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"mock.tool","arguments":"{"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_math","function":{"arguments":"\"value\":1}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		}
		return scriptedSSE(
			`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	tools := &testToolExecutor{
		definitions: []api.ToolDetailResponse{
			{Key: "mock.tool", Name: "mock.tool", Parameters: map[string]any{"type": "object"}},
		},
		result: ToolExecutionResult{Output: "ok"},
	}
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}}},
		newTestModelRegistry("http://mock.local"),
		tools,
		NewNoopSandboxClient(),
		client,
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

	var toolCalls []DeltaToolCall
	for {
		event, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if toolCall, ok := event.(DeltaToolCall); ok {
			toolCalls = append(toolCalls, toolCall)
		}
	}

	if len(toolCalls) != 1 {
		t.Fatalf("expected one streamed tool call after id arrives, got %#v", toolCalls)
	}
	if toolCalls[0].ID != "call_math" {
		t.Fatalf("expected real toolCallId to be reused, got %#v", toolCalls[0])
	}
	if toolCalls[0].ArgsDelta != "{\"value\":1}" {
		t.Fatalf("expected buffered arguments to flush once id arrives, got %#v", toolCalls[0])
	}
}

func TestLLMAgentEngineFailsRunWhenToolCallIDNeverArrives(t *testing.T) {
	client := newScriptedHTTPClient(func(*http.Request) scriptedHTTPResponse {
		return scriptedSSE(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"mock.tool","arguments":"{\"value\":1}"}}]},"finish_reason":"tool_calls"}]}`,
			`[DONE]`,
		)
	})
	tools := &testToolExecutor{
		definitions: []api.ToolDetailResponse{
			{Key: "mock.tool", Name: "mock.tool", Parameters: map[string]any{"type": "object"}},
		},
		result: ToolExecutionResult{Output: "ok"},
	}
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}}},
		newTestModelRegistry("http://mock.local"),
		tools,
		NewNoopSandboxClient(),
		client,
	)
	stream, err := engine.Stream(context.Background(), api.QueryRequest{Message: "call tool"}, QuerySession{
		RunID:     "run_tool_missing_id",
		ChatID:    "chat_tool_missing_id",
		ModelKey:  "mock-model",
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
	// Skip react-step marker if present
	if _, isMarker := event.(DeltaStageMarker); isMarker {
		event, err = stream.Next()
		if err != nil {
			t.Fatalf("next after marker: %v", err)
		}
	}
	runErr, ok := event.(DeltaError)
	if !ok {
		t.Fatalf("expected DeltaError, got %#v", event)
	}
	if runErr.Error["code"] != "missing_tool_call_id" {
		t.Fatalf("expected missing_tool_call_id, got %#v", runErr.Error)
	}
	if len(tools.invocations) != 0 {
		t.Fatalf("expected tool execution to be skipped, got %#v", tools.invocations)
	}
	if _, err := stream.Next(); err != io.EOF {
		t.Fatalf("expected eof after tool id error, got %v", err)
	}
}

func TestLLMAgentEngineLogsRawChunksAndParsedContent(t *testing.T) {
	rawHello := `{"choices":[{"delta":{"content":"hello "}}]}`
	rawWorld := `{"choices":[{"delta":{"content":"world"},"finish_reason":"stop"}]}`
	client := newScriptedHTTPClient(
		func(*http.Request) scriptedHTTPResponse {
			return scriptedSSE(rawHello, rawWorld, `[DONE]`)
		},
	)
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{
			Logging:  config.LoggingConfig{LLMInteraction: config.LLMInteractionLoggingConfig{Enabled: true}},
			Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}},
		},
		newTestModelRegistry("http://mock.local"),
		&testToolExecutor{},
		NewNoopSandboxClient(),
		client,
	)

	var stream AgentStream
	logs := captureLogOutput(t, func() {
		var err error
		stream, err = engine.Stream(context.Background(), api.QueryRequest{Message: "hi"}, QuerySession{
			RunID:    "run_logs_plain",
			ChatID:   "chat_logs_plain",
			ModelKey: "mock-model",
		})
		if err != nil {
			t.Fatalf("stream query: %v", err)
		}
		defer stream.Close()
		drainAgentStream(t, stream)
	})

	if !strings.Contains(logs, "[llm][run:run_logs_plain][raw_chunk] "+rawHello) {
		t.Fatalf("expected raw hello chunk in logs, got %s", logs)
	}
	if !strings.Contains(logs, "[llm][run:run_logs_plain][raw_chunk] "+rawWorld) {
		t.Fatalf("expected raw world chunk in logs, got %s", logs)
	}
	if !strings.Contains(logs, "[llm][run:run_logs_plain][parsed_content] hello") {
		t.Fatalf("expected parsed hello content in logs, got %s", logs)
	}
	if !strings.Contains(logs, "[llm][run:run_logs_plain][parsed_content] world") {
		t.Fatalf("expected parsed world content in logs, got %s", logs)
	}
	if !strings.Contains(logs, "[llm][run:run_logs_plain][parsed_finish_reason] stop") {
		t.Fatalf("expected parsed finish reason in logs, got %s", logs)
	}
}

func TestLLMAgentEngineLogsOutgoingRequestBodyWithToolSchema(t *testing.T) {
	client := newScriptedHTTPClient(
		func(*http.Request) scriptedHTTPResponse {
			return scriptedSSE(`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
		},
	)
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{
			Logging:  config.LoggingConfig{LLMInteraction: config.LLMInteractionLoggingConfig{Enabled: true}},
			Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}},
		},
		newTestModelRegistry("http://mock.local"),
		&testToolExecutor{
			definitions: []api.ToolDetailResponse{
				{
					Key:  "mock.tool",
					Name: "mock.tool",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"value": map[string]any{
								"type": "number",
							},
						},
					},
				},
			},
		},
		NewNoopSandboxClient(),
		client,
	)

	var stream AgentStream
	logs := captureLogOutput(t, func() {
		var err error
		stream, err = engine.Stream(context.Background(), api.QueryRequest{Message: "hello schema"}, QuerySession{
			RunID:     "run_request_body",
			ChatID:    "chat_request_body",
			ModelKey:  "mock-model",
			ToolNames: []string{"mock.tool"},
		})
		if err != nil {
			t.Fatalf("stream query: %v", err)
		}
		defer stream.Close()
		drainAgentStream(t, stream)
	})

	if !strings.Contains(logs, "[llm][run:run_request_body][request_summary]") {
		t.Fatalf("expected request summary log, got %s", logs)
	}
	if !strings.Contains(logs, "toolCount=1") {
		t.Fatalf("expected request summary to report one tool, got %s", logs)
	}
	if !strings.Contains(logs, "[llm][run:run_request_body][request_body]") {
		t.Fatalf("expected request body log, got %s", logs)
	}
	for _, needle := range []string{`"messages":[`, `"tools":[`, `"parameters":{`, `"name":"mock.tool"`, `"value":{"type":"number"}`, `hello schema`} {
		if !strings.Contains(logs, needle) {
			t.Fatalf("expected request body log to contain %q, got %s", needle, logs)
		}
	}
}

func TestLLMAgentEngineUsesCanonicalBuiltinMemoryToolNameInRequestBody(t *testing.T) {
	client := newScriptedHTTPClient(
		func(*http.Request) scriptedHTTPResponse {
			return scriptedSSE(`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
		},
	)
	store, err := memory.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	backendTools, err := NewRuntimeToolExecutor(
		config.Config{
			Logging: config.LoggingConfig{LLMInteraction: config.LLMInteractionLoggingConfig{Enabled: true}},
			Memory:  config.MemoryConfig{SearchDefaultLimit: 10},
			Defaults: config.DefaultsConfig{
				React: config.ReactDefaultsConfig{MaxSteps: 4},
			},
		},
		NewNoopSandboxClient(),
		store,
	)
	if err != nil {
		t.Fatalf("new runtime tool executor: %v", err)
	}
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{
			Logging:  config.LoggingConfig{LLMInteraction: config.LLMInteractionLoggingConfig{Enabled: true}},
			Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}},
		},
		newTestModelRegistry("http://mock.local"),
		backendTools,
		NewNoopSandboxClient(),
		client,
	)

	var stream AgentStream
	logs := captureLogOutput(t, func() {
		stream, err = engine.Stream(context.Background(), api.QueryRequest{Message: "inspect canonical memory tool"}, QuerySession{
			RunID:     "run_memory_builtin_request",
			ChatID:    "chat_memory_builtin_request",
			ModelKey:  "mock-model",
			AgentKey:  "agent-a",
			ToolNames: []string{"_memory_read_"},
		})
		if err != nil {
			t.Fatalf("stream query: %v", err)
		}
		defer stream.Close()
		drainAgentStream(t, stream)
	})

	if !strings.Contains(logs, `"name":"_memory_read_"`) {
		t.Fatalf("expected canonical builtin name in request body log, got %s", logs)
	}
	if strings.Contains(logs, `"name":"memory_read"`) {
		t.Fatalf("did not expect legacy memory_read name in request body log, got %s", logs)
	}
	if !strings.Contains(logs, `"sort":{"description":"可选排序，支持 recent 或 importance，默认 recent","type":"string"}`) &&
		!strings.Contains(logs, `"sort":{"description":"可选排序`) {
		t.Fatalf("expected Java memory read schema in request body log, got %s", logs)
	}
}

func TestLLMAgentEngineMasksLLMInteractionLogsWhenConfigured(t *testing.T) {
	client := newScriptedHTTPClient(
		func(*http.Request) scriptedHTTPResponse {
			return scriptedSSE(`{"choices":[{"delta":{"content":"secret-text"},"finish_reason":"stop"}]}`, `[DONE]`)
		},
	)
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{
			Logging: config.LoggingConfig{
				LLMInteraction: config.LLMInteractionLoggingConfig{
					Enabled:       true,
					MaskSensitive: true,
				},
			},
			Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}},
		},
		newTestModelRegistry("http://mock.local"),
		&testToolExecutor{},
		NewNoopSandboxClient(),
		client,
	)

	var stream AgentStream
	logs := captureLogOutput(t, func() {
		var err error
		stream, err = engine.Stream(context.Background(), api.QueryRequest{Message: "hi"}, QuerySession{
			RunID:    "run_logs_masked",
			ChatID:   "chat_logs_masked",
			ModelKey: "mock-model",
		})
		if err != nil {
			t.Fatalf("stream query: %v", err)
		}
		defer stream.Close()
		drainAgentStream(t, stream)
	})

	if !strings.Contains(logs, "[llm][run:run_logs_masked][raw_chunk] [masked chars=") {
		t.Fatalf("expected masked raw chunk log, got %s", logs)
	}
	if !strings.Contains(logs, "[llm][run:run_logs_masked][parsed_content] [masked chars=") {
		t.Fatalf("expected masked parsed content log, got %s", logs)
	}
	if !strings.Contains(logs, "[llm][run:run_logs_masked][request_body]") || !strings.Contains(logs, "body=[masked chars=") {
		t.Fatalf("expected masked request body log, got %s", logs)
	}
	if strings.Contains(logs, "secret-text") {
		t.Fatalf("expected secret text to stay masked, got %s", logs)
	}
}

func TestLLMAgentEngineSanitizesSensitiveLLMInteractionLogs(t *testing.T) {
	engine := &LLMAgentEngine{
		cfg: config.Config{
			Logging: config.LoggingConfig{
				LLMInteraction: config.LLMInteractionLoggingConfig{Enabled: true},
			},
		},
	}

	logs := captureLogOutput(t, func() {
		engine.logRawChunk("run_sanitized", "Bearer sk-secret-value\ntoken=demo-token\napiKey=demo-key\nsecret=demo-secret")
		engine.logParsedDelta("run_sanitized", "content", "line one\nline two")
		engine.logOutgoingRequest(
			"run_sanitized",
			ProviderDefinition{Key: "mock"},
			ModelDefinition{ModelID: "mock-model"},
			"http://mock.local/v1/chat/completions",
			nil,
			nil,
			"auto",
			[]byte(`{"messages":[{"role":"user","content":"Bearer sk-secret-value token=demo-token apiKey=demo-key secret=demo-secret"}]}`),
		)
	})

	if strings.Contains(logs, "sk-secret-value") || strings.Contains(logs, "demo-token") || strings.Contains(logs, "demo-key") || strings.Contains(logs, "demo-secret") {
		t.Fatalf("expected sensitive values to be redacted, got %s", logs)
	}
	if !strings.Contains(logs, "[redacted]") {
		t.Fatalf("expected redacted marker in logs, got %s", logs)
	}
	if !strings.Contains(logs, "line one\\nline two") {
		t.Fatalf("expected newline escaping in logs, got %s", logs)
	}
}

func TestLLMAgentEngineWarnsWhenRequestedToolsProduceNoToolSpecs(t *testing.T) {
	client := newScriptedHTTPClient(
		func(*http.Request) scriptedHTTPResponse {
			return scriptedSSE(`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
		},
	)
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{
			Logging:  config.LoggingConfig{LLMInteraction: config.LLMInteractionLoggingConfig{Enabled: true}},
			Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}},
		},
		newTestModelRegistry("http://mock.local"),
		&testToolExecutor{},
		NewNoopSandboxClient(),
		client,
	)

	var stream AgentStream
	logs := captureLogOutput(t, func() {
		var err error
		stream, err = engine.Stream(context.Background(), api.QueryRequest{Message: "missing tool"}, QuerySession{
			RunID:     "run_missing_schema",
			ChatID:    "chat_missing_schema",
			ModelKey:  "mock-model",
			ToolNames: []string{"missing.tool"},
		})
		if err != nil {
			t.Fatalf("stream query: %v", err)
		}
		defer stream.Close()
		drainAgentStream(t, stream)
	})

	if !strings.Contains(logs, "[llm][run:run_missing_schema][warning]") {
		t.Fatalf("expected missing tool schema warning, got %s", logs)
	}
	if !strings.Contains(logs, "missing.tool") {
		t.Fatalf("expected warning to include requested tool name, got %s", logs)
	}
	if strings.Contains(logs, `"tools":[`) {
		t.Fatalf("expected request body to omit tools when no specs exist, got %s", logs)
	}
}

func TestLLMAgentEngineStreamsReasoningThenContent(t *testing.T) {
	client := newScriptedHTTPClient(
		func(*http.Request) scriptedHTTPResponse {
			return scriptedSSE(
				`{"choices":[{"delta":{"reasoning_content":"thinking..."}}]}`,
				`{"choices":[{"delta":{"reasoning_content":" more thoughts"}}]}`,
				`{"choices":[{"delta":{"content":"hello"}}]}`,
				`{"choices":[{"delta":{"content":" world"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		},
	)
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{Defaults: config.DefaultsConfig{React: config.ReactDefaultsConfig{MaxSteps: 4}}},
		newTestModelRegistry("http://mock.local"),
		&testToolExecutor{},
		NewNoopSandboxClient(),
		client,
	)
	stream, err := engine.Stream(context.Background(), api.QueryRequest{Message: "hi"}, QuerySession{
		RunID:    "run_reason",
		ChatID:   "chat_reason",
		ModelKey: "mock-model",
	})
	if err != nil {
		t.Fatalf("stream query: %v", err)
	}
	defer stream.Close()

	var seenTypes []string
	var reasoningTexts []string
	var contentTexts []string
	for {
		event, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		typeName := deltaTypeName(event)
		seenTypes = append(seenTypes, typeName)
		if r, ok := event.(DeltaReasoning); ok {
			reasoningTexts = append(reasoningTexts, r.Text)
		}
		if c, ok := event.(DeltaContent); ok {
			contentTexts = append(contentTexts, c.Text)
		}
	}

	assertContainsType(t, seenTypes, "reasoning.delta")
	assertContainsType(t, seenTypes, "content.delta")
	assertContainsType(t, seenTypes, "run.complete")

	if len(reasoningTexts) != 2 {
		t.Fatalf("expected 2 reasoning deltas, got %d: %v", len(reasoningTexts), reasoningTexts)
	}
	if reasoningTexts[0] != "thinking..." || reasoningTexts[1] != " more thoughts" {
		t.Fatalf("unexpected reasoning texts: %v", reasoningTexts)
	}
	if len(contentTexts) != 2 {
		t.Fatalf("expected 2 content deltas, got %d: %v", len(contentTexts), contentTexts)
	}
	if contentTexts[0] != "hello" || contentTexts[1] != " world" {
		t.Fatalf("unexpected content texts: %v", contentTexts)
	}
}

func TestLLMAgentEngineSupportsAnthropicStreamingAndToolUse(t *testing.T) {
	var mu sync.Mutex
	callCount := 0
	client := newScriptedHTTPClient(func(req *http.Request) scriptedHTTPResponse {
		mu.Lock()
		defer mu.Unlock()
		callCount++

		if got := req.URL.String(); got != "https://provider.example.com/v1/messages" {
			t.Fatalf("unexpected anthropic endpoint: %s", got)
		}
		if got := req.Header.Get("X-Api-Key"); got != "test-key" {
			t.Fatalf("expected x-api-key header, got %q", got)
		}
		if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("expected anthropic-version header, got %q", got)
		}
		if got := req.Header.Get("anthropic-beta"); got != "tools-2024-04-04" {
			t.Fatalf("expected model header override, got %q", got)
		}

		var request map[string]any
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request["model"] != "reasoner-model" {
			t.Fatalf("unexpected model in request: %#v", request)
		}
		if request["max_tokens"] != float64(2048) {
			t.Fatalf("expected max_tokens from stage settings, got %#v", request["max_tokens"])
		}
		thinking, _ := request["thinking"].(map[string]any)
		if thinking["budget_tokens"] != float64(3072) {
			t.Fatalf("expected compat-overridden thinking budget, got %#v", thinking)
		}
		messages, _ := request["messages"].([]any)
		if callCount == 1 {
			if len(messages) == 0 {
				t.Fatalf("expected initial anthropic messages, got %#v", request)
			}
			return scriptedEventSSE(
				sseFrame{"message_start", `{"type":"message_start"}`},
				sseFrame{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`},
				sseFrame{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thinking..."}}`},
				sseFrame{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"mock.tool"}}`},
				sseFrame{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"value\":1}"}}`},
				sseFrame{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`},
				sseFrame{"message_stop", `{"type":"message_stop"}`},
			)
		}

		foundToolResult := false
		for _, raw := range messages {
			msg, _ := raw.(map[string]any)
			if msg["role"] != "user" {
				continue
			}
			content, _ := msg["content"].([]any)
			if len(content) == 0 {
				continue
			}
			firstBlock, _ := content[0].(map[string]any)
			if firstBlock["type"] == "tool_result" && firstBlock["tool_use_id"] == "toolu_1" {
				foundToolResult = true
				break
			}
		}
		if !foundToolResult {
			t.Fatalf("expected second anthropic request to include tool_result block first, got %#v", request)
		}

		return scriptedEventSSE(
			sseFrame{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`},
			sseFrame{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`},
			sseFrame{"message_stop", `{"type":"message_stop"}`},
		)
	})

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
		result: ToolExecutionResult{Output: "tool-ok"},
	}
	engine := NewLLMAgentEngineWithHTTPClient(
		config.Config{Defaults: config.DefaultsConfig{MaxTokens: 4096, React: config.ReactDefaultsConfig{MaxSteps: 4}}},
		&ModelRegistry{
			providers: map[string]ProviderDefinition{
				"compat_provider": {
					Key:     "compat_provider",
					BaseURL: "https://provider.example.com",
					APIKey:  "test-key",
					Protocols: map[string]ProtocolDefinition{
						"ANTHROPIC": {
							EndpointPath: "/v1/messages",
							Headers:      map[string]string{"anthropic-version": "2023-06-01"},
							Compat: map[string]any{
								"request": map[string]any{
									"whenReasoningEnabled": map[string]any{
										"thinking": map[string]any{"budget_tokens": 3072},
									},
								},
							},
						},
					},
				},
			},
			models: map[string]ModelDefinition{
				"reasoner-model": {
					Key:      "reasoner-model",
					Provider: "compat_provider",
					Protocol: "ANTHROPIC",
					ModelID:  "reasoner-model",
					Headers:  map[string]string{"anthropic-beta": "tools-2024-04-04"},
				},
			},
		},
		tools,
		NewNoopSandboxClient(),
		client,
	)

	stream, err := engine.Stream(context.Background(), api.QueryRequest{Message: "use tool"}, QuerySession{
		RunID:     "run_anthropic",
		ChatID:    "chat_anthropic",
		ModelKey:  "reasoner-model",
		ToolNames: []string{"mock.tool"},
		ResolvedStageSettings: PlanExecuteSettings{
			Execute: StageSettings{
				ReasoningEnabled: true,
				ReasoningEffort:  "HIGH",
				MaxTokens:        2048,
			},
		},
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
		seenTypes = append(seenTypes, deltaTypeName(event))
	}

	if len(tools.invocations) != 1 || tools.invocations[0]["value"] != float64(1) {
		t.Fatalf("expected anthropic tool invocation args, got %#v", tools.invocations)
	}
	assertContainsType(t, seenTypes, "reasoning.delta")
	assertContainsType(t, seenTypes, "tool.args")
	assertContainsType(t, seenTypes, "tool.result")
	assertContainsType(t, seenTypes, "content.delta")
	assertContainsType(t, seenTypes, "run.complete")
}

type scriptedHTTPResponse struct {
	statusCode int
	body       string
	headers    map[string]string
}

type scriptedRoundTripper struct {
	handler func(*http.Request) scriptedHTTPResponse
}

func (r scriptedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	response := r.handler(req)
	statusCode := response.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	header := make(http.Header)
	for key, value := range response.headers {
		header.Set(key, value)
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "text/event-stream")
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(response.body)),
		Request:    req,
	}, nil
}

func newScriptedHTTPClient(handler func(*http.Request) scriptedHTTPResponse) *http.Client {
	return &http.Client{Transport: scriptedRoundTripper{handler: handler}}
}

func scriptedSSE(frames ...string) scriptedHTTPResponse {
	var builder strings.Builder
	for _, frame := range frames {
		builder.WriteString("data: ")
		builder.WriteString(frame)
		builder.WriteString("\n\n")
	}
	return scriptedHTTPResponse{
		statusCode: http.StatusOK,
		body:       builder.String(),
		headers:    map[string]string{"Content-Type": "text/event-stream"},
	}
}

type sseFrame struct {
	event string
	data  string
}

func scriptedEventSSE(frames ...sseFrame) scriptedHTTPResponse {
	var builder strings.Builder
	for _, frame := range frames {
		if strings.TrimSpace(frame.event) != "" {
			builder.WriteString("event: ")
			builder.WriteString(frame.event)
			builder.WriteString("\n")
		}
		builder.WriteString("data: ")
		builder.WriteString(frame.data)
		builder.WriteString("\n\n")
	}
	return scriptedHTTPResponse{
		statusCode: http.StatusOK,
		body:       builder.String(),
		headers:    map[string]string{"Content-Type": "text/event-stream"},
	}
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

func assertContainsType(t *testing.T, seen []string, want string) {
	t.Helper()
	for _, value := range seen {
		if value == want {
			return
		}
	}
	t.Fatalf("expected event type %s in %#v", want, seen)
}

func deltaTypeName(delta AgentDelta) string {
	switch delta.(type) {
	case DeltaContent:
		return "content.delta"
	case DeltaReasoning:
		return "reasoning.delta"
	case DeltaToolCall:
		return "tool.args"
	case DeltaToolEnd:
		return "tool.end"
	case DeltaToolResult:
		return "tool.result"
	case DeltaFinishReason:
		return "run.complete"
	case DeltaError:
		return "run.error"
	default:
		return "unknown"
	}
}

func drainAgentStream(t *testing.T, stream AgentStream) {
	t.Helper()
	for {
		if _, err := stream.Next(); err != nil {
			if err == io.EOF {
				return
			}
			t.Fatalf("drain stream: %v", err)
		}
	}
}

func captureLogOutput(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	originalPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
		log.SetPrefix(originalPrefix)
	}()
	fn()
	return buf.String()
}
