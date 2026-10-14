package llm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func TestAnthropicStreamPreservesWhitespaceThroughSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name, kind, field string
		chunks            []string
	}{
		{
			name: "markdown table", kind: "text_delta", field: "text",
			chunks: []string{"操作结果如下：", "\n\n", "| 项目 | 状态 |", "\n", "| --- | --- |", "\n", "| 桌面宠物 | 已打开 |", "\n\n", "均已回读确认。"},
		},
		{
			name: "thinking word boundaries", kind: "thinking_delta", field: "thinking",
			chunks: []string{"I", " see", " the issue now", " - ", "my", " previous table", " had a", " broken separator row.", "\n\n"},
		},
		{
			name: "mixed whitespace", kind: "text_delta", field: "text",
			chunks: []string{"  indented", " ", "text  ", "\r\n", "\t", "next line", "", "\n"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, s := anthropicUsageTestStream()
			var wire strings.Builder
			for _, chunk := range tc.chunks {
				payload, err := json.Marshal(map[string]any{
					"type": "content_block_delta", "index": 0,
					"delta": map[string]any{"type": tc.kind, tc.field: chunk},
				})
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&wire, "event: content_block_delta\r\ndata: %s\r\n\r\n", payload)
			}
			reader := bufio.NewReader(strings.NewReader(wire.String()))
			for range tc.chunks {
				event, raw, err := readSSEFrame(reader)
				if err != nil {
					t.Fatal(err)
				}
				if done, err := p.ConsumeChunk(s, event, raw); done || err != nil {
					t.Fatalf("done=%v err=%v", done, err)
				}
			}
			want := strings.Join(tc.chunks, "")
			got := s.currentTurn.content.String()
			rawBytes := s.currentTurn.observation.RawContentBytes
			if tc.kind == "thinking_delta" {
				got = s.currentTurn.reasoning.String()
				rawBytes = s.currentTurn.observation.RawReasoningBytes
			}
			if got != want || rawBytes != len(want) {
				t.Fatalf("provider text changed: got %q (%d raw bytes), want %q (%d bytes)", got, rawBytes, want, len(want))
			}
			mapper := NewDeltaMapper("run-whitespace", "chat-whitespace", contracts.Budget{}, nil, nil)
			assembler := stream.NewAssembler(stream.StreamRequest{RunID: "run-whitespace", ChatID: "chat-whitespace"})
			var emissions []stream.EventEmission
			for _, delta := range s.pending {
				for _, input := range mapper.Map(delta) {
					emissions = append(emissions, assembler.ConsumeEmissions(input)...)
				}
			}
			emissions = append(emissions, assembler.CompleteEmissions()...)
			family := "content"
			if tc.kind == "thinking_delta" {
				family = "reasoning"
			}
			var assembled strings.Builder
			var snapshots []string
			for _, emission := range emissions {
				// Exercise the JSON transport round trip too: escaped newlines must
				// remain part of the payload rather than SSE frame delimiters.
				raw, err := json.Marshal(emission.Event.Payload)
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				switch emission.Event.Type {
				case family + ".delta":
					assembled.WriteString(payload["delta"].(string))
				case family + ".snapshot":
					snapshots = append(snapshots, payload["text"].(string))
				}
			}
			if assembled.String() != want || !reflect.DeepEqual(snapshots, []string{want}) {
				t.Fatalf("stream text changed: deltas=%q snapshots=%q want=%q", assembled.String(), snapshots, want)
			}
		})
	}
}

func TestAnthropicStreamPreservesWhitespaceInsideToolArguments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
	}{
		{name: "whitespace-only fragment", chunks: []string{`{ "message": "hello`, " ", `world" }`}},
		{
			name: "shell command word boundaries",
			chunks: []string{
				`{"command":"find `, `\"/tmp/AI建设文档\" `, `-name `, `\"*.kbx\""}`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, s := anthropicUsageTestStream()
			s.allowToolUse = true
			if _, err := p.ConsumeChunk(s, "content_block_start", `{"index":0,"content_block":{"type":"tool_use","id":"call-1","name":"example","input":{}}}`); err != nil {
				t.Fatal(err)
			}
			for _, chunk := range tc.chunks {
				raw, err := json.Marshal(map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": chunk}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.ConsumeChunk(s, "content_block_delta", string(raw)); err != nil {
					t.Fatal(err)
				}
			}
			calls, err := s.currentTurn.materializeToolCalls()
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || calls[0].Function.Arguments != strings.Join(tc.chunks, "") {
				t.Fatalf("tool argument fragments changed: %#v", calls)
			}
		})
	}
}

func TestReasoningDetailsPreserveWhitespace(t *testing.T) {
	want := []string{"I", " see", " ", "the issue.", "\n\n"}
	details := make([]map[string]any, 0, len(want))
	for _, text := range want {
		details = append(details, map[string]any{"text": text})
	}
	if got := extractReasoningDetailTexts(details); !reflect.DeepEqual(got, want) {
		t.Fatalf("reasoning detail fragments changed: got %q want %q", got, want)
	}
}
