package llm

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
)

func TestOutputRepetitionCharacterGateAndChunkBoundaries(t *testing.T) {
	text := strings.Repeat("让我先看看技能目录。", 600)
	for _, chunk := range []int{1, 7, 256, len(text)} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			var d outputRepetitionDetector
			found := false
			for start := 0; start < len(text); start += chunk {
				_, found = d.append(text[start:min(start+chunk, len(text))])
				if found {
					break
				}
			}
			if !found || d.total != 4000 {
				t.Fatalf("found=%v chars=%d, want first check at 4000 Unicode characters", found, d.total)
			}
		})
	}
	var d outputRepetitionDetector
	if _, found := d.append(strings.Repeat("中", 3999)); found {
		t.Fatal("activated before 4000 characters")
	}
	if _, found := d.append("中"); !found {
		t.Fatal("did not activate at 4000 characters")
	}
}

func TestOutputRepetitionRequiresSustainedExactTail(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	normal := make([]rune, 20000)
	for i := range normal {
		normal[i] = rune(0x4e00 + rng.Intn(2000))
	}
	var d outputRepetitionDetector
	if _, found := d.append(string(normal)); found {
		t.Fatal("non-repetitive long content rejected")
	}
	// A repeated heading with different data must not be treated as an exact loop.
	var rows strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&rows, "| 字段名称 | 第 %06d 项的值 |\n", i)
	}
	d = outputRepetitionDetector{}
	if _, found := d.append(rows.String()); found {
		t.Fatal("table with changing values rejected")
	}
	d = outputRepetitionDetector{}
	if _, found := d.append(string(normal[:3900]) + strings.Repeat("abcdefgh", 100)); found {
		t.Fatal("repeat shorter than 1024 characters rejected")
	}
	if _, found := d.append(strings.Repeat("abcdefgh", 200)); !found {
		t.Fatal("sustained repeat missed")
	}
	// Also require eight copies, not just a long duplicated paragraph.
	block := string(normal[:200])
	d = outputRepetitionDetector{}
	if _, found := d.append(string(normal[:4000]) + strings.Repeat(block, 7)); found {
		t.Fatal("fewer than eight copies rejected")
	}
	if _, found := d.append(strings.Repeat(block, 5)); !found {
		t.Fatal("repeated paragraph missed")
	}
}

func TestOutputRepetitionChannelsAndAttemptsAreIndependent(t *testing.T) {
	s := newRetryTestStream(&retryProtocolStub{}, 3)
	s.currentTurn = &providerTurnStream{}
	s.appendContentDelta(strings.Repeat("正文重复测试内容", 375))
	s.appendReasoningDelta(strings.Repeat("推理重复测试内容", 375), "reasoning_content")
	if s.currentTurn.outputGuardErr != nil {
		t.Fatal("combined channel counts triggered detection")
	}
	s.appendReasoningDelta(strings.Repeat("推理重复测试内容", 125), "reasoning_content")
	if s.currentTurn.outputGuardErr == nil {
		t.Fatal("reasoning threshold did not trigger")
	}
	s.currentTurn = &providerTurnStream{}
	s.appendReasoningDelta(strings.Repeat("推理重复测试内容", 375), "reasoning_content")
	if s.currentTurn.outputGuardErr != nil {
		t.Fatal("new provider attempt inherited detector state")
	}
}

type repetitionTestBody struct {
	io.Reader
	closed bool
}

func (b *repetitionTestBody) Close() error { b.closed = true; return nil }

func TestOutputRepetitionAtThinkTagFlushPreservesErrorTrace(t *testing.T) {
	s := newRetryTestStream(&retryProtocolStub{}, 3)
	s.protocolConfig.Compat = map[string]any{"response": map[string]any{"reasoningFormat": "THINK_TAG_CONTENT"}}
	trace := &llmChatTrace{enabled: true, path: filepath.Join(t.TempDir(), "trace.json"), payload: map[string]any{}}
	s.currentTurn = &providerTurnStream{trace: trace}
	text := strings.Repeat("重复推理文本检查", 500) // Exactly 4,000 code points.
	s.appendCompatContent("<think>" + text)
	if s.currentTurn.outputGuardErr != nil {
		t.Fatal("think-tag parser should still buffer the end of this chunk")
	}
	if err := s.finishCurrentTurn(); err == nil {
		t.Fatal("final buffered characters bypassed repetition detection")
	}
	if err := s.handleModelAttemptError(s.currentTurn.outputGuardErr); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(trace.path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	response := record["response"].(map[string]any)
	if record["status"] != "error" || response["reasoning_content"] != text {
		t.Fatal("failed reasoning missing from opt-in error trace")
	}
	diagnostics := record["diagnostics"].(map[string]any)
	if diagnostics["errorCode"] != string(apperrors.CodeModelOutputRepetition) || diagnostics["outputRepetition"] == nil {
		t.Fatalf("missing repetition diagnostics: %#v", diagnostics)
	}
	if len(s.messages) != 1 {
		t.Fatal("failed output entered continuation messages")
	}
}

func TestOutputRepetitionCancelsWithoutRetryOrToolCommit(t *testing.T) {
	for _, format := range []string{"content", "reasoning_content", "reasoning_details", "think_tag", "anthropic"} {
		t.Run(format, func(t *testing.T) {
			s := newRetryTestStream(&retryProtocolStub{}, 3)
			s.allowToolUse = true
			s.modelCall = &pendingModelCall{runSeq: 1, attempt: 1, maxAttempts: 4}
			s.runLLMChatCompletionCount = 1
			s.protocol = &openAIProtocol{engine: s.engine}
			text := strings.Repeat("现在开始读取技能目录。", 500)
			delta := map[string]any{}
			switch format {
			case "content", "reasoning_content":
				delta[format] = text
			case "reasoning_details":
				s.protocolConfig.Compat = map[string]any{"response": map[string]any{"reasoningFormat": "REASONING_DETAILS_TEXT"}}
				delta[format] = []any{map[string]any{"text": text}}
			case "think_tag":
				s.protocolConfig.Compat = map[string]any{"response": map[string]any{"reasoningFormat": "THINK_TAG_CONTENT"}}
				delta["content"] = "<think>" + text + "</think>should not be emitted"
			}
			// Even a same-frame tool call + finish must never be committed.
			delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "bad", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{}`}}}
			payload, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": "tool_calls"}}})
			if err != nil {
				t.Fatal(err)
			}
			frame := "data: " + string(payload) + "\n\n"
			if format == "anthropic" {
				s.model.Protocol = "ANTHROPIC"
				s.protocol = &anthropicProtocol{engine: s.engine}
				payload, _ = json.Marshal(map[string]any{"delta": map[string]any{"type": "thinking_delta", "thinking": text}})
				frame = "event: content_block_delta\ndata: " + string(payload) + "\n\n"
			}
			body := &repetitionTestBody{Reader: strings.NewReader(frame)}
			cancelled := false
			s.currentTurn = &providerTurnStream{body: body, reader: bufio.NewReader(body), cancel: func() { cancelled = true }}
			var terminal error
			discarded := false
			for i := 0; i < 20; i++ {
				d, err := s.Next()
				if err != nil {
					terminal = err
					break
				}
				switch d := d.(type) {
				case contracts.DeltaModelTurnDiscard:
					discarded = true
					if d.Retrying || d.Reason != string(apperrors.CodeModelOutputRepetition) {
						t.Fatalf("discard=%#v", d)
					}
				case contracts.DeltaModelTurnCommit, contracts.DeltaToolCall, contracts.DeltaContextCompact:
					t.Fatalf("unexpected delta after repetition: %T", d)
				}
			}
			var appErr *apperrors.Error
			if !errors.As(terminal, &appErr) || appErr.Code() != apperrors.CodeModelOutputRepetition {
				t.Fatalf("terminal=%v", terminal)
			}
			if appErr.Payload()["retryable"] != false || !cancelled || !body.closed || !discarded {
				t.Fatalf("cancelled=%v closed=%v discarded=%v payload=%#v", cancelled, body.closed, discarded, appErr.Payload())
			}
			if len(s.messages) != 1 || s.currentTurn != nil || s.modelCall != nil || len(s.queuedToolCalls) != 0 || s.compactWork != nil {
				t.Fatal("failed turn retained or scheduled more work")
			}
		})
	}
}
