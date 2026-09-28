package llm

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
)

func consumeResponsesFixture(s *llmRunStream, frames ...string) (bool, error) {
	s.currentTurn.reader = bufio.NewReader(strings.NewReader("data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"))
	for range frames {
		if done, err := s.consumeCurrentTurn(); done || err != nil {
			return done, err
		}
	}
	return false, nil
}

func TestResponsesCompletedEmptyOutputUsesDoneItems(t *testing.T) {
	for _, output := range []string{`,"output":[]`, `,"output":null`, ``} {
		t.Run(output, func(t *testing.T) {
			_, s := responseTestStream()
			turn := s.currentTurn
			executor := &recordingToolExecutor{}
			s.engine.tools = executor
			s.allowToolUse = true
			s.maxSteps = 2
			s.postToolHook = func(string, string) contracts.PostToolHookResult { return contracts.PostToolStop }
			done, err := consumeResponsesFixture(s,
				`{"type":"response.created","response":{"id":"resp1"}}`,
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs1"}}`,
				`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"**Assessing**"}`,
				`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs1","encrypted_content":"cipher1","summary":[{"type":"summary_text","text":"**Assessing**"}]}}`,
				`{"type":"response.output_item.added","output_index":1,"item":{"type":"reasoning","id":"rs2"}}`,
				`{"type":"response.reasoning_summary_text.delta","output_index":1,"summary_index":0,"delta":"**Inspecting**"}`,
				`{"type":"response.output_item.done","output_index":1,"item":{"type":"reasoning","id":"rs2","encrypted_content":"cipher2","summary":[{"type":"summary_text","text":"**Inspecting**"}]}}`,
				`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc1","call_id":"call1","name":"datetime","arguments":""}}`,
				`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{"}`,
				`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"}"}`,
				`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","status":"completed","id":"fc1","call_id":"call1","name":"datetime","arguments":"{}"}}`,
				`{"type":"response.completed","response":{"id":"resp1","status":"completed"`+output+`,"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}}`,
			)
			if err != nil || !done {
				t.Fatalf("done=%v err=%v", done, err)
			}
			if turn.observation.ResponsesRecoveredItems != 3 || s.runTotalTokens != 15 {
				t.Fatalf("recovery=%d tokens=%d", turn.observation.ResponsesRecoveredItems, s.runTotalTokens)
			}
			commits, results := 0, 0
			var reasoning strings.Builder
			for {
				delta, err := s.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				switch d := delta.(type) {
				case contracts.DeltaModelTurnCommit:
					commits++
					if d.ResponseID != "resp1" || len(d.EncryptedReasoning) != 2 || d.EncryptedReasoning[0].EncryptedText != "cipher1" || d.EncryptedReasoning[1].EncryptedText != "cipher2" {
						t.Fatalf("lost opaque reasoning or ID: %#v", d)
					}
				case contracts.DeltaToolResult:
					results++
				case contracts.DeltaReasoning:
					reasoning.WriteString(d.Text)
				}
			}
			if commits != 1 || results != 1 || len(executor.invocations) != 1 || reasoning.String() != "**Assessing**\n\n**Inspecting**" {
				t.Fatalf("commits=%d results=%d calls=%d reasoning=%q", commits, results, len(executor.invocations), reasoning.String())
			}
		})
	}
}

func TestResponsesDoneRecoveryRefusesIncompleteOrConflictingStreams(t *testing.T) {
	toolDone := `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc1","call_id":"c1","name":"datetime","arguments":"{}"}}`
	completed := `{"type":"response.completed","response":{"id":"resp1","status":"completed","output":[]}}`
	for name, frames := range map[string][]string{
		"added_without_done":      {`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc1"}}`, completed},
		"sparse_indices":          {strings.Replace(toolDone, `"output_index":0`, `"output_index":1`, 1), completed},
		"unaccounted_delta":       {toolDone, `{"type":"response.output_text.delta","output_index":1,"delta":"lost"}`, completed},
		"incomplete_item":         {strings.Replace(toolDone, `"id":"fc1"`, `"id":"fc1","status":"incomplete"`, 1), completed},
		"changed_identity":        {`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"different"}}`, toolDone, completed},
		"arguments_disagree":      {`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"x\":1}"}`, toolDone, completed},
		"incomplete_terminal":     {toolDone, `{"type":"response.incomplete","response":{"id":"resp1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`},
		"failed_terminal":         {toolDone, `{"type":"response.failed","response":{"id":"resp1","status":"failed","error":{"code":"server_error","message":"failed"}}}`},
		"done_marker_only":        {toolDone, `[DONE]`},
		"partial_terminal":        {toolDone, strings.ReplaceAll(strings.ReplaceAll(toolDone, `"output_index":0`, `"output_index":1`), `c1`, `c2`), `{"type":"response.completed","response":{"id":"resp1","status":"completed","output":[{"type":"function_call","id":"fc1","call_id":"c1","name":"datetime","arguments":"{}"}]}}`},
		"duplicate_call_id":       {toolDone, strings.Replace(toolDone, `"output_index":0`, `"output_index":1`, 1), completed},
		"invalid_arguments":       {strings.Replace(toolDone, `"arguments":"{}"`, `"arguments":"[]"`, 1), completed},
		"later_message_disagrees": {toolDone, `{"type":"response.output_text.delta","output_index":1,"delta":"lost"}`, `{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"m1","content":[{"type":"output_text","text":"different"}]}}`, completed},
	} {
		t.Run(name, func(t *testing.T) {
			_, s := responseTestStream()
			s.allowToolUse = true
			if _, err := consumeResponsesFixture(s, frames...); err == nil {
				t.Fatal("accepted incomplete or conflicting stream")
			}
			for _, delta := range s.pending {
				switch delta.(type) {
				case contracts.DeltaModelTurnCommit, contracts.DeltaToolCall:
					t.Fatal("published tools or committed invalid stream")
				}
			}
			if len(s.queuedToolCalls) != 0 {
				t.Fatal("queued invalid tool call")
			}
		})
	}
}

func TestResponsesSummaryPartsKeepParagraphs(t *testing.T) {
	for _, output := range []string{`[]`, `[{"type":"reasoning","id":"rs1","summary":[{"type":"summary_text","text":"**Assessing**"},{"type":"summary_text","text":"**Inspecting**"}]},{"type":"message","id":"m1","content":[{"type":"output_text","text":"OK"}]}]`} {
		_, s := responseTestStream()
		done, err := consumeResponsesFixture(s,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"**Assessing**"}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":1,"delta":"**Inspect"}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":1,"delta":"ing**"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs1","summary":[{"type":"summary_text","text":"**Assessing**"},{"type":"summary_text","text":"**Inspecting**"}]}}`,
			`{"type":"response.output_text.delta","output_index":1,"delta":"O"}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"m1","content":[{"type":"output_text","text":"OK"}]}}`,
			`{"type":"response.completed","response":{"id":"resp1","status":"completed","output":`+output+`}}`,
		)
		if err != nil || !done {
			t.Fatalf("done=%v err=%v", done, err)
		}
		var reasoning, text strings.Builder
		for _, delta := range s.pending {
			switch d := delta.(type) {
			case contracts.DeltaReasoning:
				reasoning.WriteString(d.Text)
			case contracts.DeltaContent:
				text.WriteString(d.Text)
			}
		}
		if reasoning.String() != "**Assessing**\n\n**Inspecting**" || text.String() != "OK" {
			t.Fatalf("reasoning=%q text=%q", reasoning.String(), text.String())
		}
	}
}
