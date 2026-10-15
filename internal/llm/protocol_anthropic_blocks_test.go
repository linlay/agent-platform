package llm

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

// splitRunes cuts text at random rune boundaries; providers may split a
// fragment anywhere between characters, including inside JSON strings.
func splitRunes(rng *rand.Rand, text string) []string {
	runes := []rune(text)
	var parts []string
	for len(runes) > 0 {
		n := 1 + rng.Intn(min(len(runes), 7))
		parts = append(parts, string(runes[:n]))
		runes = runes[n:]
	}
	return parts
}

func anthropicFrame(t *testing.T, value map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Any split of a multi-block response must reassemble to the provider's
// exact text, with a paragraph break only between distinct thinking blocks.
func TestAnthropicStreamReassemblesAnySplitAcrossBlocks(t *testing.T) {
	const (
		reasoning = "I see the issue now - my previous table had a broken separator row."
		progress  = "Next I will run find  on the 文档 directory."
		answer    = "操作结果如下：\n\n| 项目 | 状态 |\n| --- | --- |\n| 桌面宠物 | 已打开 |\n\n  indented line\tand tab "
		arguments = `{"command":"find \"/tmp/AI建设文档\" -name \"*.kbx\"","note":"  two  spaces\nnext line "}`
	)
	rng := rand.New(rand.NewSource(20261014))
	for iteration := 0; iteration < 300; iteration++ {
		p, s := anthropicUsageTestStream()
		s.allowToolUse = true
		consume := func(event string, value map[string]any) {
			t.Helper()
			if done, err := p.ConsumeChunk(s, event, anthropicFrame(t, value)); done || err != nil {
				t.Fatalf("iteration %d %s: done=%v err=%v", iteration, event, done, err)
			}
		}
		textBlock := func(index int, blockType, field, deltaType, text string) {
			parts := splitRunes(rng, text)
			start := ""
			// Compatible relays may put the first fragment on the block start.
			if rng.Intn(2) == 0 {
				start, parts = parts[0], parts[1:]
			}
			consume("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": blockType, field: start}})
			for _, part := range parts {
				consume("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": deltaType, field: part}})
			}
			if blockType == "thinking" {
				consume("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "signature_delta", "signature": "sig"}})
			}
			consume("content_block_stop", map[string]any{"index": index})
		}
		textBlock(0, "thinking", "thinking", "thinking_delta", reasoning)
		textBlock(1, "thinking", "thinking", "thinking_delta", progress)
		textBlock(2, "text", "text", "text_delta", answer)
		consume("content_block_start", map[string]any{"index": 3, "content_block": map[string]any{"type": "tool_use", "id": "call-1", "name": "bash", "input": map[string]any{}}})
		for _, part := range splitRunes(rng, arguments) {
			consume("content_block_delta", map[string]any{"index": 3, "delta": map[string]any{"type": "input_json_delta", "partial_json": part}})
		}
		consume("content_block_stop", map[string]any{"index": 3})

		turn := s.currentTurn
		if got := turn.content.String(); got != answer {
			t.Fatalf("iteration %d content changed:\n got %q\nwant %q", iteration, got, answer)
		}
		if got, want := turn.reasoning.String(), reasoning+"\n\n"+progress; got != want {
			t.Fatalf("iteration %d reasoning changed:\n got %q\nwant %q", iteration, got, want)
		}
		if turn.observation.RawContentBytes != len(answer) || turn.observation.RawReasoningBytes != len(reasoning)+len(progress) {
			t.Fatalf("iteration %d raw bytes: content=%d reasoning=%d", iteration, turn.observation.RawContentBytes, turn.observation.RawReasoningBytes)
		}
		calls, err := turn.materializeToolCalls()
		if err != nil || len(calls) != 1 || calls[0].Function.Arguments != arguments {
			t.Fatalf("iteration %d tool arguments changed: %#v err=%v", iteration, calls, err)
		}

		mapper := NewDeltaMapper("run-blocks", "chat-blocks", contracts.Budget{}, nil, nil)
		assembler := stream.NewAssembler(stream.StreamRequest{RunID: "run-blocks", ChatID: "chat-blocks"})
		var content, thinking, toolArgs strings.Builder
		for _, delta := range s.pending {
			for _, input := range mapper.Map(delta) {
				for _, emission := range assembler.ConsumeEmissions(input) {
					switch emission.Event.Type {
					case "content.delta":
						content.WriteString(emission.Event.Payload["delta"].(string))
					case "reasoning.delta":
						thinking.WriteString(emission.Event.Payload["delta"].(string))
					case "tool.args":
						toolArgs.WriteString(emission.Event.Payload["delta"].(string))
					}
				}
			}
		}
		if content.String() != answer || thinking.String() != reasoning+"\n\n"+progress || toolArgs.String() != arguments {
			t.Fatalf("iteration %d stream events changed: content=%q reasoning=%q tool args=%q", iteration, content.String(), thinking.String(), toolArgs.String())
		}
	}
}

func TestAnthropicThinkingBlockSeparatorReusesTrailingNewlines(t *testing.T) {
	for _, tc := range []struct{ first, want string }{
		{first: "done.\n\n", want: "done.\n\nnext"},
		{first: "done.\n", want: "done.\n\nnext"},
		{first: "done.", want: "done.\n\nnext"},
	} {
		p, s := anthropicUsageTestStream()
		for index, text := range []string{tc.first, "next"} {
			raw := anthropicFrame(t, map[string]any{"index": index, "delta": map[string]any{"type": "thinking_delta", "thinking": text}})
			if _, err := p.ConsumeChunk(s, "content_block_delta", raw); err != nil {
				t.Fatal(err)
			}
		}
		if got := s.currentTurn.reasoning.String(); got != tc.want {
			t.Fatalf("reasoning after %q = %q, want %q", tc.first, got, tc.want)
		}
	}
}

func TestAnthropicStreamAcceptsQuotedBlockIndex(t *testing.T) {
	p, s := anthropicUsageTestStream()
	s.allowToolUse = true
	frames := [][2]string{
		{"content_block_start", `{"index":"1","content_block":{"type":"tool_use","id":"call-1","name":"bash","input":{}}}`},
		{"content_block_delta", `{"index":"1","delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls -la\"}"}}`},
	}
	for _, frame := range frames {
		if _, err := p.ConsumeChunk(s, frame[0], frame[1]); err != nil {
			t.Fatal(err)
		}
	}
	calls, err := s.currentTurn.materializeToolCalls()
	if err != nil || len(calls) != 1 || calls[0].Function.Arguments != `{"command":"ls -la"}` {
		t.Fatalf("quoted index tool call: %#v err=%v", calls, err)
	}
}

func TestAnthropicContextWindowStopIsTruncation(t *testing.T) {
	turn := &providerTurnStream{finishReason: "model_context_window_exceeded"}
	turn.content.WriteString("partial answer")
	payload := modelResponseFailure(turn)
	if payload == nil || payload["code"] != string(apperrors.CodeModelOutputLimit) {
		t.Fatalf("expected output-limit failure, got %#v", payload)
	}
	if !strings.Contains(payload["message"].(string), "上下文窗口") {
		t.Fatalf("unexpected message: %v", payload["message"])
	}
	if reason := payload["diagnostics"].(map[string]any)["reason"]; reason != "context_window_limit" {
		t.Fatalf("unexpected diagnostic reason: %v", reason)
	}

	for _, tc := range []struct{ name, arguments string }{
		{name: "complete tool", arguments: `{"command":"echo safe"}`},
		{name: "partial tool", arguments: `{"command":"echo partial`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, s := anthropicUsageTestStream()
			s.allowToolUse = true
			executor := &recordingToolExecutor{}
			s.engine.tools = executor
			for _, frame := range []struct{ event, data string }{
				{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"partial answer"}}`},
				{"content_block_start", `{"index":1,"content_block":{"type":"tool_use","id":"call-1","name":"bash","input":{}}}`},
				{"content_block_delta", anthropicFrame(t, map[string]any{"index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": tc.arguments}})},
			} {
				if done, err := p.ConsumeChunk(s, frame.event, frame.data); done || err != nil {
					t.Fatalf("%s: done=%v err=%v", frame.event, done, err)
				}
			}
			done, err := p.ConsumeChunk(s, "message_delta", `{"delta":{"stop_reason":"model_context_window_exceeded"},"usage":{"output_tokens":100}}`)
			if !done || err != nil {
				t.Fatalf("done=%v err=%v", done, err)
			}
			var errors, discards int
			for _, delta := range s.pending {
				switch v := delta.(type) {
				case contracts.DeltaToolResult, contracts.DeltaModelTurnCommit:
					t.Fatalf("truncated tool was executed or committed: %#v", delta)
				case contracts.DeltaModelTurnDiscard:
					discards++
				case contracts.DeltaError:
					errors++
					if v.Error["code"] != string(apperrors.CodeModelOutputLimit) || v.Error["diagnostics"].(map[string]any)["reason"] != "context_window_limit" {
						t.Fatalf("incorrect context-window failure: %#v", v.Error)
					}
				}
			}
			if errors != 1 || discards != 1 || !s.finished || s.currentTurn != nil || s.modelCall != nil || len(s.queuedToolCalls) != 0 || len(executor.invocations) != 0 || s.runToolCallCount != 0 {
				t.Fatalf("context-window stop did not discard the tool and terminate: errors=%d discards=%d queued=%d executed=%d", errors, discards, len(s.queuedToolCalls), len(executor.invocations))
			}
		})
	}
}

func TestConvertMessagesToAnthropicKeepsContentWhitespace(t *testing.T) {
	var reloaded map[string]any
	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":"  look\n"},{"type":"image_url","image_url":{"url":"data:image/png;base64,cG5nLWJ5dGVz"}}]}`), &reloaded); err != nil {
		t.Fatal(err)
	}
	_, out, err := convertMessagesToAnthropic([]openAIMessage{
		{Role: "system", Content: "  system prompt \n"},
		rawMessageToOpenAI(reloaded, false),
		{Role: "assistant", Content: "  I will read it.\n", ToolCalls: []openAIToolCall{{ID: "call-1", Type: "function", Function: openAIFunctionCall{Name: "file_read", Arguments: `{"path":"a.py"}`}}}},
		{Role: "tool", ToolCallID: "call-1", Content: "    def main():\n        pass\n"},
		{Role: "tool", ToolCallID: "call-2", Content: []map[string]any{{"type": "text", "text": "digest"}}},
		{Role: "user", Content: " \n\t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("expected user, assistant, tool results; got %#v", out)
	}
	user := out[0]["content"].([]map[string]any)
	if len(user) != 2 || user[0]["text"] != "  look\n" || user[1]["type"] != "image" {
		t.Fatalf("reloaded user parts were not converted verbatim: %#v", user)
	}
	assistant := out[1]["content"].([]map[string]any)
	if assistant[0]["text"] != "I will read it." {
		t.Fatalf("assistant text should stay trimmed: %#v", assistant[0])
	}
	results := out[2]["content"].([]map[string]any)
	if results[0]["content"] != "    def main():\n        pass\n" {
		t.Fatalf("tool result whitespace changed: %#v", results[0]["content"])
	}
	digest, ok := results[1]["content"].([]map[string]any)
	if !ok || len(digest) != 1 || digest[0]["text"] != "digest" {
		t.Fatalf("structured tool result was not converted to blocks: %#v", results[1]["content"])
	}
}
