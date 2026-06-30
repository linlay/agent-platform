package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestLLMChatTraceWritesSimpleCompletion(t *testing.T) {
	recordDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"))
	}))
	defer server.Close()

	engine := newTraceTestEngine(t, recordDir, server.URL, nil)
	req := api.QueryRequest{ChatID: "chat_1", Message: "Hi"}
	stream, err := engine.newRunStream(context.Background(), req, traceTestSessionWithSystemCache(t, engine, req), false)
	if err != nil {
		t.Fatalf("newRunStream: %v", err)
	}
	drainTraceTestStream(t, stream)

	trace := readTraceFile(t, recordDir, 1)
	if trace["status"] != "ok" {
		t.Fatalf("status=%#v want ok trace=%#v", trace["status"], trace)
	}
	if trace["runSeq"] != float64(1) || trace["chatId"] != "chat_1" || trace["runId"] != "run_trace" {
		t.Fatalf("unexpected metadata: %#v", trace)
	}
	if trace["sentAt"] == "" || trace["responseStartedAt"] == "" || trace["completedAt"] == "" {
		t.Fatalf("expected timing fields: %#v", trace)
	}
	request := trace["request"].(map[string]any)
	if request["model"] != "mock-model-id" {
		t.Fatalf("unexpected request body: %#v", request)
	}
	if _, ok := trace["tools"]; ok {
		t.Fatalf("did not expect top-level tools field: %#v", trace["tools"])
	}
	response := trace["response"].(map[string]any)
	if response["content"] != "hello world" || response["finishReason"] != "stop" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if _, err := os.Stat(traceFilePath(recordDir, "chat_1", 2)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("did not expect run_trace_002.json, stat err=%v", err)
	}
}

func TestRunStreamUsesSessionCurrentMessages(t *testing.T) {
	recordDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer server.Close()

	engine := newTraceTestEngine(t, recordDir, server.URL, nil)
	session := traceTestSession()
	session.CurrentMessages = []map[string]any{{
		"role":    "user",
		"content": "canonical model-side message",
	}}
	req := api.QueryRequest{ChatID: "chat_1", Message: "raw user message"}
	stream, err := engine.newRunStream(context.Background(), req, traceTestSessionWithSystemCache(t, engine, req, session), false)
	if err != nil {
		t.Fatalf("newRunStream: %v", err)
	}
	drainTraceTestStream(t, stream)

	trace := readTraceFile(t, recordDir, 1)
	request := trace["request"].(map[string]any)
	rawMessages, _ := request["messages"].([]any)
	if len(rawMessages) < 2 {
		t.Fatalf("expected system + current messages, got %#v", request["messages"])
	}
	current, _ := rawMessages[len(rawMessages)-1].(map[string]any)
	if current["role"] != "user" || current["content"] != "canonical model-side message" {
		t.Fatalf("expected request to use session CurrentMessages, got %#v", rawMessages)
	}
}

func TestLLMChatTraceWritesToolLoopFiles(t *testing.T) {
	recordDir := t.TempDir()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"datetime\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer server.Close()

	toolDef := []api.ToolDetailResponse{{
		Name:        "datetime",
		Description: "Get current time",
		Parameters:  map[string]any{"type": "object"},
	}}
	executor := &recordingToolExecutor{
		defs:   toolDef,
		result: contracts.ToolExecutionResult{Output: "2026-05-26T00:00:00Z", ExitCode: 0},
	}
	engine := newTraceTestEngine(t, recordDir, server.URL, executor)
	req := api.QueryRequest{ChatID: "chat_1", Message: "Use tool"}
	stream, err := engine.newRunStream(context.Background(), req, traceTestSessionWithSystemCache(t, engine, req), true)
	if err != nil {
		t.Fatalf("newRunStream: %v", err)
	}
	drainTraceTestStream(t, stream)

	first := readTraceFile(t, recordDir, 1)
	if first["status"] != "ok" {
		t.Fatalf("first status=%#v trace=%#v", first["status"], first)
	}
	if _, ok := first["tools"]; ok {
		t.Fatalf("did not expect top-level tools field: %#v", first["tools"])
	}
	firstRequest := first["request"].(map[string]any)
	tools := firstRequest["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected request.tools to remain available, got %#v", firstRequest["tools"])
	}
	toolCalls := first["toolCalls"].([]any)
	if len(toolCalls) != 1 || toolCalls[0].(map[string]any)["toolName"] != "datetime" {
		t.Fatalf("unexpected tool calls: %#v", toolCalls)
	}
	response := first["response"].(map[string]any)
	responseToolCalls := response["tool_calls"].([]any)
	if len(responseToolCalls) != 1 {
		t.Fatalf("expected response.tool_calls, got %#v", response)
	}
	responseToolCall := responseToolCalls[0].(map[string]any)
	function := responseToolCall["function"].(map[string]any)
	if responseToolCall["id"] != "call_1" || function["name"] != "datetime" || function["arguments"] != "{}" {
		t.Fatalf("unexpected response.tool_calls: %#v", responseToolCalls)
	}
	toolResults := first["toolResults"].([]any)
	if len(toolResults) != 1 || !strings.Contains(toolResults[0].(map[string]any)["content"].(string), "2026-05-26") {
		t.Fatalf("unexpected tool results: %#v", toolResults)
	}
	entries, err := os.ReadDir(filepath.Join(recordDir, "chat_1", ".llm-records"))
	if err != nil {
		t.Fatalf("read llm records dir: %v", err)
	}
	if len(entries) != 2 || entries[0].Name() != "run_trace_001.json" || entries[1].Name() != "run_trace_002.json" {
		t.Fatalf("unexpected trace file ordering: %#v", entries)
	}
	second := readTraceFile(t, recordDir, 2)
	request := second["request"].(map[string]any)
	messages := request["messages"].([]any)
	foundToolMessage := false
	for _, raw := range messages {
		msg := raw.(map[string]any)
		if msg["role"] == "tool" && msg["name"] == "datetime" {
			foundToolMessage = true
		}
	}
	if !foundToolMessage {
		t.Fatalf("expected second request to include tool result message: %#v", messages)
	}
}

func TestLLMChatTraceWritesReasoningContent(t *testing.T) {
	recordDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"...\",\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer server.Close()

	engine := newTraceTestEngine(t, recordDir, server.URL, nil)
	req := api.QueryRequest{ChatID: "chat_1", Message: "Hi"}
	stream, err := engine.newRunStream(context.Background(), req, traceTestSessionWithSystemCache(t, engine, req), false)
	if err != nil {
		t.Fatalf("newRunStream: %v", err)
	}
	drainTraceTestStream(t, stream)

	trace := readTraceFile(t, recordDir, 1)
	response := trace["response"].(map[string]any)
	if response["content"] != "answer" || response["reasoning_content"] != "thinking..." {
		t.Fatalf("unexpected response with reasoning: %#v", response)
	}
}

func TestLLMChatTraceWritesInterruptInfo(t *testing.T) {
	recordDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	control := contracts.NewRunControl(context.Background(), "run_trace")
	ctx := contracts.WithRunControl(context.Background(), control)
	engine := newTraceTestEngine(t, recordDir, server.URL, nil)
	req := api.QueryRequest{ChatID: "chat_1", Message: "Hi"}
	stream, err := engine.newRunStream(ctx, req, traceTestSessionWithSystemCache(t, engine, req), false)
	if err != nil {
		t.Fatalf("newRunStream: %v", err)
	}
	defer stream.Close()

	if _, err := stream.Next(); err != nil {
		t.Fatalf("stream first delta: %v", err)
	}
	control.Interrupt(contracts.InterruptInfo{
		Source:    contracts.InterruptSourceHTTPAPI,
		Reason:    contracts.InterruptReasonUserCancelled,
		Detail:    "interrupt requested by HTTP API",
		RequestID: "request_1",
		ChatID:    "chat_1",
	})
	for i := 0; i < 4; i++ {
		_, err := stream.Next()
		if errors.Is(err, contracts.ErrRunInterrupted) {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("stream after interrupt: %v", err)
		}
		if i == 3 {
			t.Fatalf("expected stream to report interruption")
		}
	}

	trace := readTraceFile(t, recordDir, 1)
	if trace["status"] != "interrupted" || trace["error"] != "run interrupted" {
		t.Fatalf("unexpected interrupted trace: %#v", trace)
	}
	interrupt := trace["interrupt"].(map[string]any)
	if interrupt["source"] != contracts.InterruptSourceHTTPAPI || interrupt["reason"] != contracts.InterruptReasonUserCancelled {
		t.Fatalf("unexpected interrupt info: %#v", interrupt)
	}
	if interrupt["detail"] != "interrupt requested by HTTP API" || interrupt["requestId"] != "request_1" || interrupt["chatId"] != "chat_1" {
		t.Fatalf("unexpected interrupt metadata: %#v", interrupt)
	}
	if interrupt["interruptedAt"] == "" {
		t.Fatalf("expected interruptedAt: %#v", interrupt)
	}
}

func TestLLMChatTraceWritesProviderError(t *testing.T) {
	recordDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provider down", http.StatusBadGateway)
	}))
	defer server.Close()

	engine := newTraceTestEngine(t, recordDir, server.URL, nil)
	req := api.QueryRequest{ChatID: "chat_1", Message: "Hi"}
	_, err := engine.newRunStream(context.Background(), req, traceTestSessionWithSystemCache(t, engine, req), false)
	if err == nil {
		t.Fatal("expected provider error")
	}
	trace := readTraceFile(t, recordDir, 1)
	if trace["status"] != "error" || !strings.Contains(trace["error"].(string), "502") {
		t.Fatalf("unexpected error trace: %#v", trace)
	}
}

func TestLLMChatTraceDisabledWritesNoFiles(t *testing.T) {
	recordDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer server.Close()

	engine := newTraceTestEngine(t, recordDir, server.URL, nil)
	engine.cfg.Logging.LLMInteraction.RecordEnabled = false
	req := api.QueryRequest{ChatID: "chat_1", Message: "Hi"}
	stream, err := engine.newRunStream(context.Background(), req, traceTestSessionWithSystemCache(t, engine, req), false)
	if err != nil {
		t.Fatalf("newRunStream: %v", err)
	}
	drainTraceTestStream(t, stream)
	entries, err := os.ReadDir(recordDir)
	if err != nil {
		t.Fatalf("read record dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no trace files, got %v", entries)
	}
}

func TestLLMChatTraceMaskSensitivePreservesMetadata(t *testing.T) {
	recordDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"secret answer\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer server.Close()

	engine := newTraceTestEngine(t, recordDir, server.URL, nil)
	engine.cfg.Logging.LLMInteraction.MaskSensitive = true
	req := api.QueryRequest{ChatID: "chat_1", Message: "sk-test-secret"}
	stream, err := engine.newRunStream(context.Background(), req, traceTestSessionWithSystemCache(t, engine, req), false)
	if err != nil {
		t.Fatalf("newRunStream: %v", err)
	}
	drainTraceTestStream(t, stream)
	trace := readTraceFile(t, recordDir, 1)
	if trace["runId"] != "run_trace" || trace["status"] != "ok" {
		t.Fatalf("expected metadata preserved: %#v", trace)
	}
	request := trace["request"].(map[string]any)
	messages := request["messages"].([]any)
	user := messages[len(messages)-1].(map[string]any)
	if !strings.HasPrefix(user["content"].(string), "[masked chars=") {
		t.Fatalf("expected user prompt masked: %#v", user)
	}
	response := trace["response"].(map[string]any)
	if !strings.HasPrefix(response["content"].(string), "[masked chars=") {
		t.Fatalf("expected response masked: %#v", response)
	}
}

func TestSafeTraceRunID(t *testing.T) {
	tests := map[string]string{
		" run_1 ":    "run_1",
		"chat/run/1": "chat_run_1",
		`chat\run\1`: "chat_run_1",
		"":           "unknown",
		".":          "unknown",
		"..":         "unknown",
	}
	for input, want := range tests {
		if got := safeTraceRunID(input); got != want {
			t.Fatalf("safeTraceRunID(%q)=%q want %q", input, got, want)
		}
	}
}

func TestTraceFileNameUsesSortableThreeDigitSequence(t *testing.T) {
	tests := map[int]string{
		0:    "abc123_001.json",
		1:    "abc123_001.json",
		10:   "abc123_010.json",
		999:  "abc123_999.json",
		1000: "abc123_999.json",
	}
	for seq, want := range tests {
		if got := traceFileName("abc123", seq); got != want {
			t.Fatalf("traceFileName(seq=%d)=%q want %q", seq, got, want)
		}
	}
}

func newTraceTestEngine(t *testing.T, recordDir string, baseURL string, executor contracts.ToolExecutor) *LLMAgentEngine {
	t.Helper()
	if executor == nil {
		executor = stubToolExecutor{}
	}
	cfg := config.Config{}
	cfg.Logging.LLMInteraction.RecordEnabled = true
	cfg.Logging.LLMInteraction.RecordDir = recordDir
	cfg.Defaults.React.MaxSteps = 4
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "providers"), 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "models"), 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	providerYAML := "key: mock\nbaseUrl: " + baseURL + "\napiKey: token\nendpointPath: /v1/chat/completions\ndefaultModel: mock-model\n"
	modelYAML := "key: mock-model\nprovider: mock\nprotocol: OPENAI\nmodelId: mock-model-id\n"
	if err := os.WriteFile(filepath.Join(root, "providers", "mock.yml"), []byte(providerYAML), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "mock.yml"), []byte(modelYAML), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	return NewLLMAgentEngineWithHTTPClient(cfg, registry, executor, nil, contracts.NewNoopSandboxClient(), serverHTTPClient())
}

func traceTestSession() contracts.QuerySession {
	return contracts.QuerySession{
		RunID:     "run_trace",
		ChatID:    "chat_1",
		RequestID: "request_1",
		AgentKey:  "agent_1",
		ModelKey:  "mock-model",
		Mode:      "react",
		ResolvedBudget: contracts.Budget{
			Model: contracts.RetryPolicy{MaxCalls: 10},
			Tool:  contracts.RetryPolicy{MaxCalls: 10},
		},
	}
}

func traceTestSessionWithSystemCache(t *testing.T, engine *LLMAgentEngine, req api.QueryRequest, sessions ...contracts.QuerySession) contracts.QuerySession {
	t.Helper()
	session := traceTestSession()
	if len(sessions) > 0 {
		session = sessions[0]
	}
	if engine == nil || engine.tools == nil {
		return session
	}
	profiles := (SystemInitProfileBuilder{Models: engine.models}).BuildSystemInitProfiles(
		session,
		req,
		engine.tools.Definitions(),
		engine.cfg.Defaults.Plan.MaxSteps,
		engine.cfg.Defaults.Plan.MaxWorkRoundsPerTask,
		engine.cfg.Prompts,
	)
	cache := make(map[string]contracts.SystemInitSnapshot, len(profiles)*2)
	for _, profile := range profiles {
		cache[profile.CacheKey] = traceSystemInitSnapshot(profile)
		if finalProfile, ok := BuildFinalSystemInitProfile(profile, session.PromptAppend, traceToolDefsFromProfile(profile)); ok {
			(SystemInitProfileBuilder{Models: engine.models}).applyRequestProfile(&finalProfile, session, req)
			cache[finalProfile.CacheKey] = traceSystemInitSnapshot(finalProfile)
		}
	}
	session.SystemInitCache = cache
	return session
}

func traceSystemInitSnapshot(profile contracts.SystemInitProfile) contracts.SystemInitSnapshot {
	return contracts.SystemInitSnapshot{
		Fingerprint:    profile.Fingerprint,
		SystemMessage:  cloneAnyMapViaJSON(profile.SystemMessage),
		Tools:          cloneAnySlice(profile.Tools),
		Model:          cloneAnyMapViaJSON(profile.Model),
		ToolChoice:     profile.ToolChoice,
		RequestOptions: cloneAnyMapViaJSON(profile.RequestOptions),
	}
}

func traceToolDefsFromProfile(profile contracts.SystemInitProfile) []api.ToolDetailResponse {
	out := make([]api.ToolDetailResponse, 0, len(profile.Tools))
	for _, raw := range profile.Tools {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		description, _ := fn["description"].(string)
		parameters, _ := fn["parameters"].(map[string]any)
		out = append(out, api.ToolDetailResponse{
			Name:        strings.TrimSpace(name),
			Description: description,
			Parameters:  cloneAnyMapViaJSON(parameters),
		})
	}
	return out
}

func drainTraceTestStream(t *testing.T, stream contracts.AgentStream) {
	t.Helper()
	defer stream.Close()
	for {
		_, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("stream next: %v", err)
		}
	}
}

func readTraceFile(t *testing.T, recordDir string, seq int) map[string]any {
	t.Helper()
	data, err := os.ReadFile(traceFilePath(recordDir, "chat_1", seq))
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode trace file: %v", err)
	}
	return decoded
}

func traceFilePath(recordDir string, chatID string, seq int) string {
	return filepath.Join(recordDir, traceRelativeFile(chatID, "run_trace", seq))
}

func serverHTTPClient() *http.Client {
	return &http.Client{}
}
