package llm

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func anthropicUsageTestStream() (*anthropicProtocol, *llmRunStream) {
	engine := &LLMAgentEngine{}
	protocol := &anthropicProtocol{engine: engine}
	model := models.ModelDefinition{Key: "haiku", ModelID: "claude-haiku-5-5", Protocol: "ANTHROPIC", ContextWindow: 128000}
	return protocol, &llmRunStream{
		engine:                         engine,
		protocol:                       protocol,
		ctx:                            context.Background(),
		session:                        contracts.QuerySession{RunID: "run-anthropic-usage", ChatID: "chat-anthropic-usage"},
		model:                          model,
		execCtx:                        &contracts.ExecutionContext{StartedAt: time.Now()},
		modelCall:                      &pendingModelCall{runSeq: 1, attempt: 1, maxAttempts: 1},
		protocolConfig:                 resolveProtocolRuntimeConfig(models.ProviderDefinition{}, model),
		currentTurn:                    &providerTurnStream{},
		runLLMChatCompletionCount:      1,
		lastCallLLMChatCompletionCount: 1,
	}
}

func TestAnthropicStreamMergesCumulativeUsageAndDisplaysSummary(t *testing.T) {
	p, s := anthropicUsageTestStream()
	for _, frame := range []struct{ event, data string }{
		{"message_start", `{"message":{"usage":{"input_tokens":21,"cache_creation_input_tokens":80,"cache_read_input_tokens":200,"output_tokens":1}}}`},
		{"content_block_delta", `{"delta":{"type":"thinking_delta","thinking":"先核对农历。"}}`},
		{"content_block_delta", `{"delta":{"type":"text_delta","text":"结果"}}`},
		{"message_delta", `{"delta":{},"usage":{"output_tokens":20}}`},
	} {
		if done, err := p.ConsumeChunk(s, frame.event, frame.data); done || err != nil {
			t.Fatalf("%s: done=%v err=%v", frame.event, done, err)
		}
	}
	if s.runTotalTokens != 0 {
		t.Fatal("intermediate cumulative usage was committed before the turn ended")
	}
	done, err := p.ConsumeChunk(s, "message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":22,"output_tokens":30,"output_tokens_details":{"thinking_tokens":12}}}`)
	if !done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if s.runPromptTokens != 302 || s.runCompletionTokens != 30 || s.runTotalTokens != 332 ||
		s.runPromptCacheHitTokens != 200 || s.runPromptCacheMissTokens != 102 || s.runReasoningTokens != 12 {
		t.Fatalf("incorrect Anthropic totals: prompt=%d output=%d total=%d hit=%d miss=%d thinking=%d",
			s.runPromptTokens, s.runCompletionTokens, s.runTotalTokens, s.runPromptCacheHitTokens, s.runPromptCacheMissTokens, s.runReasoningTokens)
	}
	summaries, snapshots := 0, 0
	for _, delta := range s.pending {
		switch v := delta.(type) {
		case contracts.DeltaReasoning:
			summaries++
			if v.Text != "先核对农历。" || v.ReasoningLabel != "thinking_delta" {
				t.Fatalf("incorrect thinking summary: %#v", v)
			}
		case contracts.DeltaUsageSnapshot:
			snapshots++
			if v.LLMReturnReasoningTokens != 12 || v.LLMReturnTotalTokens != 332 {
				t.Fatalf("incorrect usage snapshot: %#v", v)
			}
		}
	}
	if summaries != 1 || snapshots != 1 {
		t.Fatalf("summaries=%d snapshots=%d, want one each", summaries, snapshots)
	}
}

func TestAnthropicOutputLimitRetainsUsageAndDiscardsPartialTools(t *testing.T) {
	p, s := anthropicUsageTestStream()
	s.allowToolUse = true
	executor := &recordingToolExecutor{}
	s.engine.tools = executor
	s.engine.cfg.Logging.LLMInteraction.RecordEnabled = true
	s.engine.cfg.Logging.LLMInteraction.RecordDir = t.TempDir()
	trace := s.newChatTrace(1, preparedProviderRequest{RequestBody: map[string]any{"max_tokens": 4096}}, "auto")
	s.currentTurn.trace = trace
	for _, frame := range []struct{ event, data string }{
		{"message_start", `{"message":{"usage":{"input_tokens":100,"output_tokens":1}}}`},
		{"content_block_delta", `{"delta":{"type":"thinking_delta","thinking":""}}`},
		{"content_block_delta", `{"delta":{"type":"text_delta","text":"准备推算。"}}`},
		{"content_block_start", `{"index":1,"content_block":{"type":"tool_use","id":"partial","name":"datetime","input":{}}}`},
		{"content_block_delta", `{"index":1,"delta":{"type":"input_json_delta","partial_json":"{\"base\":"}}`},
	} {
		if done, err := p.ConsumeChunk(s, frame.event, frame.data); done || err != nil {
			t.Fatalf("%s: done=%v err=%v", frame.event, done, err)
		}
	}
	done, err := p.ConsumeChunk(s, "message_delta", `{"delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4096,"output_tokens_details":{"thinking_tokens":3980}}}`)
	if !done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if s.runCompletionTokens != 4096 || s.runReasoningTokens != 3980 || s.runTotalTokens != 4196 {
		t.Fatalf("truncated turn lost usage: output=%d thinking=%d total=%d", s.runCompletionTokens, s.runReasoningTokens, s.runTotalTokens)
	}
	errors, discards := 0, 0
	for _, delta := range s.pending {
		switch v := delta.(type) {
		case contracts.DeltaToolResult:
			t.Fatal("partial tool call produced an execution result")
		case contracts.DeltaModelTurnCommit:
			t.Fatal("partial tool call was committed to history")
		case contracts.DeltaModelTurnDiscard:
			discards++
		case contracts.DeltaError:
			errors++
			if v.Error["code"] != "model_output_limit" {
				t.Fatalf("incorrect error: %#v", v.Error)
			}
		}
	}
	if errors != 1 || discards != 1 {
		t.Fatalf("errors=%d discards=%d, want one each", errors, discards)
	}
	if len(executor.invocations) != 0 || s.runToolCallCount != 0 || s.modelCall != nil || len(s.queuedToolCalls) != 0 {
		t.Fatal("truncated turn executed a tool or scheduled a follow-up model call")
	}
	raw, err := os.ReadFile(trace.path)
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string]any
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	response := recorded["response"].(map[string]any)
	usage := response["usage"].(map[string]any)
	if usage["completion_tokens"] != float64(4096) ||
		usage["completion_tokens_details"].(map[string]any)["reasoning_tokens"] != float64(3980) {
		t.Fatalf("trace lost output and thinking counts: %#v", usage)
	}
	diagnostics := recorded["diagnostics"].(map[string]any)["usage"].(map[string]any)
	if diagnostics["reasoningTokens"] != float64(3980) {
		t.Fatalf("diagnostics lost thinking token count: %#v", diagnostics)
	}
}

func TestAnthropicUsageZeroAndMissingFields(t *testing.T) {
	p, s := anthropicUsageTestStream()
	if done, err := p.ConsumeChunk(s, "message_start", `{"message":{"usage":{"input_tokens":10,"output_tokens":1}}}`); done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if done, err := p.ConsumeChunk(s, "message_delta", `{"delta":{},"usage":{"output_tokens":0}}`); done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if s.currentTurn.usage.CompletionTokens != 0 || s.currentTurn.usage.PromptTokens != 10 {
		t.Fatalf("explicit zero or omitted input fields were misread: %#v", s.currentTurn.usage)
	}
	if s.currentTurn.usage.CompletionTokensDetails.ReasoningTokens != 0 {
		t.Fatal("thinking usage must not be estimated from visible text")
	}
	s.commitPendingTurnUsage()
	s.commitPendingTurnUsage()
	if s.runTotalTokens != 10 {
		t.Fatalf("usage committed twice: %d", s.runTotalTokens)
	}
}
