package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/llm"
	"agent-platform/internal/stream"
)

type mcpHistoryRoundTrip struct {
	result   ToolExecutionResult
	live     stream.EventData
	replayed stream.EventData
	history  map[string]any
}

// Drive a fake MCP result through the real router, stream mapper, JSONL
// writer, store reopen, model-history loader and cold replay.
func runMCPHistoryRoundTrip(t *testing.T, payload map[string]any) mcpHistoryRoundTrip {
	t.Helper()
	const chatID, runID, toolID, toolName = "mcp-chat", "mcp-run", "mcp-call", "mcp_remote_search"
	def := api.ToolDetailResponse{Name: toolName, Meta: map[string]any{"sourceType": "mcp", "serverKey": "fake"}}
	router := mustNewToolRouter(t, stubBackendToolExecutor{}, outputSchemaMCPClient{payload: payload}, outputSchemaToolCatalog{def: def}, nil)
	result, err := router.Invoke(context.Background(), toolName, nil, &ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	store, err := chat.NewFileStoreAtStartup(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnsureChat(chatID, "agent", "query"); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now().UnixMilli()
	if err := store.OnRunStarted(chat.RunStart{ChatID: chatID, RunID: runID, AgentKey: "agent", StartedAtMillis: startedAt}); err != nil {
		t.Fatal(err)
	}
	writer := chat.NewStepWriter(store, chatID, runID, "GENERAL")
	assembler := stream.NewAssembler(stream.StreamRequest{RunID: runID, ChatID: chatID, AgentKey: "agent", Message: "query", StartedAtMillis: startedAt})
	for _, emission := range assembler.BootstrapEmissions() {
		writer.OnEvent(emission.Event.Data())
	}
	mapper := llm.NewDeltaMapper(runID, chatID, Budget{}, nil, nil)
	out := mcpHistoryRoundTrip{result: result}
	for _, delta := range []AgentDelta{
		DeltaToolCall{ID: toolID, Name: toolName, ArgsDelta: "{}"},
		DeltaToolEnd{ToolIDs: []string{toolID}},
		DeltaToolResult{ToolID: toolID, ToolName: toolName, Result: result},
	} {
		for _, input := range mapper.Map(delta) {
			for _, emission := range assembler.ConsumeEmissions(input) {
				data := emission.Event.Data()
				writer.OnEvent(data)
				if data.Type == "tool.result" {
					out.live = data
				}
			}
		}
	}
	writer.Flush()
	if err := writer.Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.OnRunCompleted(chat.RunCompletion{ChatID: chatID, RunID: runID, AgentKey: "agent", AssistantText: "done", FinishReason: "complete", StartedAtMillis: startedAt, UpdatedAtMillis: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = chat.NewFileStoreAtStartup(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	messages, err := store.LoadRawMessages(chatID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message["role"] == "tool" {
			out.history = message
		}
	}
	if out.history == nil {
		t.Fatal("missing restored tool message")
	}
	if _, leaked := out.history["_modelOutput"]; leaked {
		t.Fatal("storage-only field entered the model message")
	}
	detail, err := store.LoadChat(chatID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range detail.Events {
		if event.Type == "tool.result" {
			out.replayed = event
		}
	}
	if out.replayed.Type == "" {
		t.Fatal("missing replayed tool result")
	}
	return out
}

func TestMCPHistoryRestoresSupplementalTextBesideStructuredContent(t *testing.T) {
	structured := map[string]any{"count": float64(2)}
	got := runMCPHistoryRoundTrip(t, map[string]any{
		"structuredContent": structured,
		"content":           []any{map[string]any{"type": "text", "text": "部分数据源查询失败"}},
	})
	if !strings.Contains(got.result.Output, "部分数据源查询失败") {
		t.Fatalf("model output lost supplemental text: %q", got.result.Output)
	}
	if got.history["content"] != got.result.Output {
		t.Fatalf("restored history differs from the model output:\nhistory=%q\nmodel=%q", got.history["content"], got.result.Output)
	}
	if MarshalJSON(got.live.Value("result")) != MarshalJSON(structured) {
		t.Fatalf("business result changed: %#v", got.live.Value("result"))
	}
	if got.live.String("modelOutput") != got.result.Output || got.replayed.String("modelOutput") != got.result.Output {
		t.Fatalf("modelOutput missing from live or replayed event: live=%#v replayed=%#v", got.live, got.replayed)
	}
}

func TestMCPHistoryKeepsBase64OutOfRestoredModelText(t *testing.T) {
	body := strings.Repeat("A", 4096)
	got := runMCPHistoryRoundTrip(t, map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "chart"},
			map[string]any{"type": "image", "mimeType": "image/png", "data": body},
		},
	})
	content, _ := got.history["content"].(string)
	if strings.Contains(got.result.Output, body) || strings.Contains(content, body) {
		t.Fatalf("base64 entered model text: output=%d history=%d chars", len(got.result.Output), len(content))
	}
	if content != got.result.Output {
		t.Fatalf("restored history differs from the model output:\nhistory=%q\nmodel=%q", content, got.result.Output)
	}
}

func TestMCPHistoryOmitsModelOutputWhenResultAlreadyCarriesIt(t *testing.T) {
	got := runMCPHistoryRoundTrip(t, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "plain answer"}},
	})
	if _, ok := got.live.Payload["modelOutput"]; ok {
		t.Fatalf("redundant modelOutput: %#v", got.live.Payload)
	}
	if got.history["content"] != "plain answer" {
		t.Fatalf("unexpected restored history: %#v", got.history)
	}
}

func TestMCPErrorEventKeepsParseableJSONWhileModelSeesDiagnostics(t *testing.T) {
	got := runMCPHistoryRoundTrip(t, map[string]any{
		"isError":           true,
		"structuredContent": map[string]any{"code": "version_conflict", "message": "read current version"},
		"content":           []any{map[string]any{"type": "text", "text": "see docs"}},
	})
	if !strings.Contains(got.result.Output, "see docs") {
		t.Fatalf("model output lost diagnostics: %q", got.result.Output)
	}
	result := AnyMapNode(got.live.Value("result"))
	output, _ := result["output"].(string)
	var business map[string]any
	if err := json.Unmarshal([]byte(output), &business); err != nil || business["code"] != "version_conflict" {
		t.Fatalf("event error output is not the platform error JSON: %q", output)
	}
	if got.live.String("modelOutput") != got.result.Output {
		t.Fatalf("modelOutput missing from error event: %#v", got.live.Payload)
	}
	if got.history["content"] != got.result.Output {
		t.Fatalf("restored history differs from the model output:\nhistory=%q\nmodel=%q", got.history["content"], got.result.Output)
	}
}

func TestNormalizeMCPResultKeepsTextWhitespace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		texts []string
		want  string
	}{
		{"single block", []string{"  first\n\tsecond  "}, "  first\n\tsecond  "},
		{"multiple blocks", []string{"  first\n", " \t ", "\tsecond  "}, "  first\n\n\n\tsecond  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks := make([]any, len(tc.texts))
			for index, text := range tc.texts {
				blocks[index] = map[string]any{"type": "text", "text": text}
			}
			result := normalizeMCPResult("read", map[string]any{"content": blocks})
			if result.Output != tc.want {
				t.Fatalf("text whitespace changed: output=%q want=%q", result.Output, tc.want)
			}
		})
	}
}

func TestMCPErrorDoesNotRepeatPaddedMessage(t *testing.T) {
	result := normalizeMCPResult("read", map[string]any{
		"isError": true,
		"content": []any{map[string]any{"type": "text", "text": "  boom\n"}},
	})
	if strings.Count(result.Output, "boom") != 1 {
		t.Fatalf("error message repeated: %q", result.Output)
	}
}
