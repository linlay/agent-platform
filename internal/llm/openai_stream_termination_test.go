package llm

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestOpenAIStreamEndCollectsTrailingUsageAndCommitsOnce(t *testing.T) {
	body := io.NopCloser(strings.NewReader(strings.Join([]string{
		`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":80}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")))
	engine := &LLMAgentEngine{}
	protocol := &openAIProtocol{engine: engine}
	stream := &llmRunStream{
		engine:         engine,
		protocol:       protocol,
		ctx:            context.Background(),
		session:        contracts.QuerySession{RunID: "run-stream-end", ChatID: "chat-stream-end"},
		model:          models.ModelDefinition{Key: "mock-model", Protocol: "OPENAI", ContextWindow: 128000},
		execCtx:        &contracts.ExecutionContext{StartedAt: time.Now()},
		modelCall:      &pendingModelCall{runSeq: 1, attempt: 1, maxAttempts: 1},
		protocolConfig: qwenStyleStreamEndCompat(50),
		currentTurn: &providerTurnStream{
			body:          body,
			reader:        bufio.NewReader(body),
			requestSentAt: time.Now(),
		},
		runLLMChatCompletionCount:      1,
		lastCallLLMChatCompletionCount: 1,
	}

	done, err := protocol.ConsumeChunk(stream, "", `{"choices":[{"delta":{"content":"hello"},"finish_reason":""}]}`)
	if err != nil || done {
		t.Fatalf("content chunk done=%v err=%v", done, err)
	}
	done, err = protocol.ConsumeChunk(stream, "", `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}}`)
	if err != nil || done {
		t.Fatalf("finish chunk should enter trailing state, done=%v err=%v", done, err)
	}
	if !stream.awaitingOpenAITerminalMetadata() {
		t.Fatal("stream did not enter awaiting-terminal-metadata state")
	}

	done, err = stream.consumeCurrentTurn()
	if err != nil || done {
		t.Fatalf("trailing usage chunk done=%v err=%v", done, err)
	}
	if stream.currentTurn == nil || stream.currentTurn.usage == nil || stream.currentTurn.usage.PromptTokensDetails.CachedTokens != 80 {
		t.Fatalf("trailing usage did not replace summary usage: %#v", stream.currentTurn)
	}
	done, err = stream.consumeCurrentTurn()
	if err != nil || !done {
		t.Fatalf("DONE terminal frame done=%v err=%v", done, err)
	}

	if stream.runPromptTokens != 100 || stream.runCompletionTokens != 5 || stream.runTotalTokens != 105 || stream.runPromptCacheHitTokens != 80 || stream.runPromptCacheMissTokens != 20 {
		t.Fatalf("unexpected committed usage: prompt=%d completion=%d total=%d hit=%d miss=%d",
			stream.runPromptTokens, stream.runCompletionTokens, stream.runTotalTokens, stream.runPromptCacheHitTokens, stream.runPromptCacheMissTokens)
	}
	if stream.runLLMChatCompletionCount != 1 || stream.lastCallLLMChatCompletionCount != 1 {
		t.Fatalf("trailing metadata changed completion count: run=%d last=%d", stream.runLLMChatCompletionCount, stream.lastCallLLMChatCompletionCount)
	}
	usageSnapshots := 0
	for _, delta := range stream.pending {
		if snapshot, ok := delta.(contracts.DeltaUsageSnapshot); ok {
			usageSnapshots++
			if snapshot.LLMReturnPromptCacheHitTokens != 80 || snapshot.LLMReturnPromptCacheMissTokens != 20 || snapshot.LLMReturnLLMChatCompletionCount != 1 {
				t.Fatalf("unexpected usage snapshot: %#v", snapshot)
			}
		}
	}
	if usageSnapshots != 1 {
		t.Fatalf("expected usage to be committed exactly once, snapshots=%d pending=%#v", usageSnapshots, stream.pending)
	}
}

func TestOpenAIFinishReasonTerminationKeepsImmediateBehavior(t *testing.T) {
	engine := &LLMAgentEngine{}
	protocol := &openAIProtocol{engine: engine}
	stream := newOpenAITerminationTestStream(engine, protocol, protocolRuntimeConfig{}, io.NopCloser(strings.NewReader("")))

	done, err := protocol.ConsumeChunk(stream, "", `{"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`)
	if err != nil || !done {
		t.Fatalf("default finish-reason chunk done=%v err=%v", done, err)
	}
	if stream.currentTurn != nil {
		t.Fatal("default termination unexpectedly waited for a trailing frame")
	}
}

func TestOpenAIStreamEndIgnoresTrailingContentAndToolCalls(t *testing.T) {
	engine := &LLMAgentEngine{}
	protocol := &openAIProtocol{engine: engine}
	stream := newOpenAITerminationTestStream(engine, protocol, qwenStyleStreamEndCompat(50), io.NopCloser(strings.NewReader("")))
	stream.currentTurn.finishReason = "stop"
	stream.currentTurn.finishSeenAt = time.Now()

	done, err := protocol.ConsumeChunk(stream, "", `{"choices":[{"delta":{"content":"IGNORED","tool_calls":[{"index":0,"id":"ignored","type":"function","function":{"name":"bash","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23,"prompt_tokens_details":{"cached_tokens":8}}}`)
	if err != nil || done {
		t.Fatalf("trailing metadata chunk done=%v err=%v", done, err)
	}
	if got := stream.currentTurn.content.String(); got != "answer" {
		t.Fatalf("trailing content was appended: %q", got)
	}
	if len(stream.currentTurn.toolCalls) != 0 {
		t.Fatalf("trailing tool calls were accumulated: %#v", stream.currentTurn.toolCalls)
	}
	if stream.currentTurn.finishReason != "stop" {
		t.Fatalf("trailing finish reason replaced the original: %q", stream.currentTurn.finishReason)
	}
	if stream.currentTurn.usage == nil || stream.currentTurn.usage.PromptTokensDetails.CachedTokens != 8 {
		t.Fatalf("trailing usage was not collected: %#v", stream.currentTurn.usage)
	}
}

func TestOpenAIStreamEndEOFAndTimeoutAfterFinishAreSuccessful(t *testing.T) {
	tests := []struct {
		name    string
		body    io.ReadCloser
		timeout int
	}{
		{name: "eof", body: io.NopCloser(strings.NewReader("")), timeout: 50},
		{name: "timeout", body: newBlockingReadCloser(), timeout: 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := &LLMAgentEngine{}
			protocol := &openAIProtocol{engine: engine}
			stream := newOpenAITerminationTestStream(engine, protocol, qwenStyleStreamEndCompat(test.timeout), test.body)
			stream.currentTurn.finishReason = "stop"
			stream.currentTurn.finishSeenAt = time.Now()

			done, err := stream.consumeCurrentTurn()
			if err != nil || !done {
				t.Fatalf("terminal %s done=%v err=%v", test.name, done, err)
			}
			if stream.currentTurn != nil || !stream.finished {
				t.Fatalf("terminal %s did not complete the turn", test.name)
			}
		})
	}
}

func TestOpenAIStreamEndUsesFinishTimeForGenerationDuration(t *testing.T) {
	start := time.Unix(100, 0)
	finish := start.Add(750 * time.Millisecond)
	engine := &LLMAgentEngine{}
	protocol := &openAIProtocol{engine: engine}
	stream := newOpenAITerminationTestStream(engine, protocol, qwenStyleStreamEndCompat(50), io.NopCloser(strings.NewReader("")))
	stream.currentTurn.requestSentAt = start.Add(-250 * time.Millisecond)
	stream.currentTurn.firstVisibleAt = start
	stream.currentTurn.finishReason = "stop"
	stream.currentTurn.finishSeenAt = finish

	done, err := stream.consumeCurrentTurn()
	if err != nil || !done {
		t.Fatalf("EOF completion done=%v err=%v", done, err)
	}
	if stream.lastCallGenerationDurationMs != 750 {
		t.Fatalf("trailing wait changed generation duration: %dms", stream.lastCallGenerationDurationMs)
	}
}

func qwenStyleStreamEndCompat(timeoutMS int) protocolRuntimeConfig {
	return protocolRuntimeConfig{Compat: map[string]any{
		"response": map[string]any{
			"stream": map[string]any{
				"termination":       "stream-end",
				"trailingTimeoutMs": timeoutMS,
			},
			"usage": map[string]any{
				"promptTokensDetails": map[string]any{
					"cacheHitTokens": map[string]any{"path": "prompt_tokens_details.cached_tokens"},
					"cacheMissTokens": map[string]any{
						"path":   nil,
						"derive": "prompt-minus-cache-hit",
					},
				},
			},
		},
	}}
}

func newOpenAITerminationTestStream(engine *LLMAgentEngine, protocol *openAIProtocol, compat protocolRuntimeConfig, body io.ReadCloser) *llmRunStream {
	turn := &providerTurnStream{
		body:          body,
		reader:        bufio.NewReader(body),
		hasMeaningful: true,
	}
	turn.content.WriteString("answer")
	return &llmRunStream{
		engine:                         engine,
		protocol:                       protocol,
		ctx:                            context.Background(),
		session:                        contracts.QuerySession{RunID: "run-termination", ChatID: "chat-termination"},
		model:                          models.ModelDefinition{Key: "mock-model", Protocol: "OPENAI", ContextWindow: 128000},
		execCtx:                        &contracts.ExecutionContext{StartedAt: time.Now()},
		modelCall:                      &pendingModelCall{runSeq: 1, attempt: 1, maxAttempts: 1},
		protocolConfig:                 compat,
		currentTurn:                    turn,
		runLLMChatCompletionCount:      1,
		lastCallLLMChatCompletionCount: 1,
	}
}

type blockingReadCloser struct {
	closed chan struct{}
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (r *blockingReadCloser) Read(_ []byte) (int, error) {
	<-r.closed
	return 0, io.EOF
}

func (r *blockingReadCloser) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}
