package llm

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/modelresponses"
	"agent-platform/internal/models"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func responseTestStream() (*responsesProtocol, *llmRunStream) {
	engine := &LLMAgentEngine{}
	p := &responsesProtocol{engine: engine}
	body := io.NopCloser(strings.NewReader(""))
	s := &llmRunStream{engine: engine, protocol: p, ctx: context.Background(), session: contracts.QuerySession{RunID: "r", ChatID: "c"}, model: models.ModelDefinition{Key: "luna", Protocol: modelresponses.Protocol, ContextWindow: 128000}, execCtx: &contracts.ExecutionContext{StartedAt: time.Now()}, modelCall: &pendingModelCall{runSeq: 1, attempt: 1, maxAttempts: 1}, currentTurn: &providerTurnStream{body: body, reader: bufio.NewReader(body)}, runLLMChatCompletionCount: 1, lastCallLLMChatCompletionCount: 1}
	return p, s
}
func TestResponsesStatelessRequestAndLegacyIsolation(t *testing.T) {
	encrypted := contracts.ReasoningPart{Type: "encrypted_text", ID: "rs1", EncryptedText: "secret-state", Summary: json.RawMessage(`[]`)}
	params := protocolStreamParams{provider: models.ProviderDefinition{BaseURL: "https://example.test", APIKey: "test"}, model: models.ModelDefinition{Key: "luna", ModelID: "gpt-6-luna", Protocol: modelresponses.Protocol}, protocolConfig: protocolRuntimeConfig{EndpointPath: "/v1/responses"}, stageSettings: contracts.StageSettings{ReasoningEnabled: true, ReasoningEffort: "HIGH", MaxOutputTokens: 2048}, messages: []openAIMessage{{Role: "system", Content: "rules"}, {Role: "user", Content: "read"}, {Role: "assistant", EncryptedReasoning: []contracts.ReasoningPart{encrypted}, ToolCalls: []openAIToolCall{{ID: "call1", Type: "function", Function: openAIFunctionCall{Name: "file_read", Arguments: `{"path":"README.md"}`}}}}, {Role: "tool", ToolCallID: "call1", Content: "Go"}}, toolSpecs: []openAIToolSpec{{Type: "function", Function: openAIToolDefinition{Name: "file_read", Parameters: map[string]any{"type": "object"}}}}}
	p := &responsesProtocol{}
	req, err := p.PrepareRequest(params)
	if err != nil {
		t.Fatal(err)
	}
	b := req.RequestBody
	if b["store"] != false || b["previous_response_id"] != nil || b["messages"] != nil || b["temperature"] != nil || b["max_output_tokens"] != float64(2048) {
		t.Fatalf("bad body %s", req.RequestBodyJSON)
	}
	input := b["input"].([]any)
	if input[2].(map[string]any)["encrypted_content"] != "secret-state" || input[3].(map[string]any)["call_id"] != "call1" || input[4].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("invalid input %v", input)
	}
	if b["tools"].([]any)[0].(map[string]any)["strict"] != false {
		t.Fatal("existing tool schema became strict")
	}
	legacy, err := (&openAIProtocol{}).PrepareRequest(params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy.RequestBodyJSON), "secret-state") || legacy.RequestBody["messages"] == nil {
		t.Fatal("ciphertext leaked into old protocol")
	}
	if requestOptionsFromPreparedBody(b)["input"] != nil {
		t.Fatal("history duplicated into options")
	}
	s := &llmRunStream{model: params.model, stageSettings: params.stageSettings}
	s.model.ContextWindow = 128000
	if err = s.prepareSummaryRequest(&req); err != nil {
		t.Fatal(err)
	}
	if req.RequestBody["max_completion_tokens"] != nil || req.RequestBody["tools"] != nil || req.RequestBody["max_output_tokens"] != 2048 {
		t.Fatal("summary request broke protocol")
	}
}
func TestResponsesStreamTextReasoningUsageCommit(t *testing.T) {
	p, s := responseTestStream()
	for _, raw := range []string{`{"type":"response.created","response":{"id":"resp1"}}`, `{"type":"response.output_text.delta","output_index":1,"delta":"hello"}`} {
		done, err := p.ConsumeChunk(s, "", raw)
		if err != nil || done {
			t.Fatalf("%v %v", done, err)
		}
	}
	done, err := p.ConsumeChunk(s, "", `{"type":"response.completed","response":{"id":"resp1","status":"completed","output":[{"type":"reasoning","id":"rs1","encrypted_content":"cipher","summary":[]},{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":12}}}}`)
	if err != nil || !done {
		t.Fatalf("%v %v", done, err)
	}
	text := ""
	commits := 0
	for _, d := range s.pending {
		switch v := d.(type) {
		case contracts.DeltaContent:
			text += v.Text
		case contracts.DeltaModelTurnCommit:
			commits++
			if v.ResponseID != "resp1" || len(v.EncryptedReasoning) != 1 {
				t.Fatalf("missing state %#v", v)
			}
		}
	}
	if text != "hello" || commits != 1 || s.runPromptCacheHitTokens != 80 || s.runReasoningTokens != 12 || s.runTotalTokens != 120 {
		t.Fatalf("bad output %q commits=%d usage=%d", text, commits, s.runTotalTokens)
	}
	if len(s.messages) != 1 || len(s.messages[0].EncryptedReasoning) != 1 {
		t.Fatal("next turn lost reasoning")
	}
}
func TestResponsesIncompleteDoesNotExecuteTools(t *testing.T) {
	p, s := responseTestStream()
	s.allowToolUse = true
	done, err := p.ConsumeChunk(s, "", `{"type":"response.incomplete","response":{"id":"resp1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]},{"type":"function_call","call_id":"c","name":"bash","arguments":"{"}]}}`)
	if !done || err != nil {
		t.Fatalf("%v %v", done, err)
	}
	sawError := false
	for _, d := range s.pending {
		switch v := d.(type) {
		case contracts.DeltaToolCall:
			t.Fatal("truncated tool executed")
		case contracts.DeltaError:
			sawError = v.Error["code"] == "model_output_limit"
		}
	}
	if !sawError {
		t.Fatal("missing output limit error")
	}
}
func TestResponsesRequiresTerminalEvent(t *testing.T) {
	_, s := responseTestStream()
	s.currentTurn.reader = bufio.NewReader(strings.NewReader("data: [DONE]\n\n"))
	if _, err := s.consumeCurrentTurn(); err == nil {
		t.Fatal("DONE accepted as response.completed")
	}
	p, s := responseTestStream()
	if _, err := p.ConsumeChunk(s, "", `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"failure"}}}`); err == nil {
		t.Fatal("failure accepted")
	}
}
func TestResponsesHistoryRoundTrip(t *testing.T) {
	raw := []map[string]any{{"role": "assistant", "_msgId": "m", "_modelKey": "luna", "reasoning_content": []any{map[string]any{"type": "encrypted_text", "id": "rs", "encrypted_text": "cipher", "summary": []any{}}}}, {"role": "assistant", "_msgId": "m", "content": "answer"}}
	merged := mergeRawMessagesByMsgID(raw)
	if len(merged) != 1 {
		t.Fatal(merged)
	}
	m := rawMessageToOpenAI(merged[0], false)
	if len(m.EncryptedReasoning) != 1 || m.Content != "answer" {
		t.Fatalf("lost state %#v", m)
	}
	input, err := modelresponses.Input([]contracts.ModelMessage{m}, "luna")
	if err != nil || len(input) != 2 {
		t.Fatalf("%v %v", input, err)
	}
	other, _ := modelresponses.Input([]contracts.ModelMessage{m}, "different-model")
	if len(other) != 1 {
		t.Fatal("cross-model state leaked")
	}
}

func TestResponsesRejectsInconsistentTerminal(t *testing.T) {
	for _, output := range []string{
		`[{"type":"reasoning","encrypted_content":"cipher"}]`,
		`[{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call","call_id":"c","name":"f","arguments":"{}"}]`,
		`[{"type":"function_call","call_id":"c","name":"f","arguments":"[]"}]`,
		`[{"type":"web_search_call"}]`,
	} {
		p, s := responseTestStream()
		if _, err := p.ConsumeChunk(s, "", `{"type":"response.completed","response":{"id":"resp","status":"completed","output":`+output+`}}`); err == nil {
			t.Fatalf("accepted %s", output)
		}
		for _, d := range s.pending {
			if _, ok := d.(contracts.DeltaModelTurnCommit); ok {
				t.Fatal("invalid turn committed")
			}
		}
	}
	p, s := responseTestStream()
	_, _ = p.ConsumeChunk(s, "", `{"type":"response.output_text.delta","output_index":0,"delta":"lost"}`)
	if _, err := p.ConsumeChunk(s, "", `{"type":"response.completed","response":{"id":"resp","status":"completed","output":[]}}`); err == nil {
		t.Fatal("lost output accepted")
	}
}

func TestResponsesEffortWithoutMapping(t *testing.T) {
	p := &responsesProtocol{}
	req, err := p.PrepareRequest(protocolStreamParams{provider: models.ProviderDefinition{BaseURL: "https://example.test", APIKey: "test"}, protocolConfig: protocolRuntimeConfig{EndpointPath: "/v1/responses"}, stageSettings: contracts.StageSettings{ReasoningEnabled: true, ReasoningEffort: "HIGH"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.RequestBody["reasoning"].(map[string]any)["effort"] != "high" {
		t.Fatal(req.RequestBody)
	}
}

func TestResponsesDefaultEndpoint(t *testing.T) {
	for _, base := range []string{"https://example.test", "https://example.test/v1"} {
		provider := models.ProviderDefinition{BaseURL: base, APIKey: "test"}
		model := models.ModelDefinition{Protocol: modelresponses.Protocol}
		req, err := (&responsesProtocol{}).PrepareRequest(protocolStreamParams{provider: provider, model: model, protocolConfig: resolveProtocolRuntimeConfig(provider, model)})
		if err != nil || req.Endpoint != "https://example.test/v1/responses" {
			t.Fatalf("%s %v", req.Endpoint, err)
		}
	}
}

func TestResponsesParallelToolsUseExistingLoop(t *testing.T) {
	p, s := responseTestStream()
	executor := &recordingToolExecutor{}
	s.engine.tools = executor
	s.allowToolUse = true
	s.maxSteps = 2
	s.postToolHook = func(string, string) contracts.PostToolHookResult { return contracts.PostToolStop }
	done, err := p.ConsumeChunk(s, "", `{"type":"response.completed","response":{"id":"resp1","status":"completed","output":[{"type":"reasoning","id":"rs1","encrypted_content":"cipher","summary":[]},{"type":"function_call","call_id":"call1","name":"datetime","arguments":"{}"},{"type":"function_call","call_id":"call2","name":"datetime","arguments":"{}"}]}}`)
	if err != nil || !done {
		t.Fatalf("%v %v", done, err)
	}
	commits, results := 0, 0
	for {
		delta, err := s.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch v := delta.(type) {
		case contracts.DeltaModelTurnCommit:
			commits++
			if v.ResponseID != "resp1" || len(v.EncryptedReasoning) != 1 {
				t.Fatal(v)
			}
		case contracts.DeltaToolResult:
			results++
		}
	}
	if commits != 1 || results != 2 || len(executor.invocations) != 2 {
		t.Fatalf("commits=%d results=%d calls=%v", commits, results, executor.invocations)
	}
	input, err := modelresponses.Input(s.messages, "luna")
	if err != nil || len(input) != 5 {
		t.Fatalf("input=%v err=%v", input, err)
	}
	if input[0].(map[string]any)["encrypted_content"] != "cipher" || input[3].(map[string]any)["call_id"] != "call1" || input[4].(map[string]any)["call_id"] != "call2" {
		t.Fatal(input)
	}
	legacy := applyOpenAIMessageCompat([]openAIMessage{{Role: "assistant", EncryptedReasoning: s.messages[0].EncryptedReasoning}, {Role: "user", Content: "hello"}}, false)
	if len(legacy) != 1 || legacy[0].Role != "user" {
		t.Fatal("empty reasoning turn leaked to old protocol")
	}
}
