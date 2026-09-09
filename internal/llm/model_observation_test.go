package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestModelObservationClassifiesProviderResponses(t *testing.T) {
	tests := []struct {
		name, body, reason, errorCode string
		status                        int
	}{
		{"empty_stop", `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, "empty_stop", "", 200},
		{"reasoning_only", `data: {"choices":[{"delta":{"reasoning_content":"private-thinking"},"finish_reason":"stop"}]}`, "reasoning_only", "", 200},
		{"unmapped_reasoning", `data: {"choices":[{"delta":{"reasoning_details":[{"text":"private-thinking"}]},"finish_reason":"stop"}]}`, "reasoning_only", "", 200},
		{"output_limit", `data: {"choices":[{"delta":{"reasoning_content":"private-thinking"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":30,"total_tokens":40}}`, "output_limit", "", 200},
		{"refusal", `data: {"choices":[{"delta":{"refusal":"private-refusal"},"finish_reason":"stop"}]}`, "content_filter_or_refusal", "", 200},
		{"done_only", `data: [DONE]`, "missing_finish_reason", "", 200},
		{"message_instead_of_delta", `data: {"choices":[{"message":{"content":"private-answer"},"finish_reason":"stop"}]}`, "non_streaming_message", "", 200},
		{"missing_tool_calls", `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, "tool_calls_missing", "", 200},
		{"normal", `data: {"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}`, "", "", 200},
		{"invalid_json", `data: {broken`, "", "provider_stream_invalid", 200},
		{"early_eof", "", "", "provider_stream_failed", 200},
		{"http_error", `{"error":{"message":"private-upstream-body"}}`, "", "provider_unavailable", 502},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-Id", "upstream-123")
				w.Header().Set("Set-Cookie", "private-cookie")
				w.WriteHeader(test.status)
				body := test.body
				if body != "" {
					body += "\n\n"
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			recordDir := t.TempDir()
			engine := newTraceTestEngine(t, recordDir, server.URL, nil)
			req := api.QueryRequest{ChatID: "chat_1", Message: "private-prompt"}
			var terminalErr error
			var content strings.Builder
			var debugStatus string
			logs := captureStandardLog(t, func() {
				stream, err := engine.newRunStream(context.Background(), req, traceTestSessionWithSystemCache(t, engine, req), false)
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				for {
					delta, err := stream.Next()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						terminalErr = err
						break
					}
					switch value := delta.(type) {
					case contracts.DeltaContent:
						content.WriteString(value.Text)
					case contracts.DeltaDebugLLMChat:
						debugStatus = value.Status
					}
				}
			})
			if requests.Load() != 1 {
				t.Fatalf("observation changed attempt count: %d", requests.Load())
			}
			trace := readTraceFile(t, recordDir, 1)
			d := trace["diagnostics"].(map[string]any)
			wantStatus := "ok"
			if test.reason != "" {
				wantStatus = "empty_response"
				if d["reason"] != test.reason || d["emptyResponse"] != true {
					t.Fatalf("unexpected empty response diagnostics: %#v", d)
				}
				if content.String() != "Model returned no assistant content." || debugStatus != wantStatus {
					t.Fatalf("content=%q debugStatus=%q", content.String(), debugStatus)
				}
				if strings.Count(logs, `"category":"llm_empty_response"`) != 1 {
					t.Fatalf("expected exactly one anomaly log: %s", logs)
				}
			} else if strings.Contains(logs, `"category":"llm_empty_response"`) {
				t.Fatalf("unexpected empty response log: %s", logs)
			}
			if test.errorCode != "" {
				wantStatus = "error"
				if terminalErr == nil || d["errorCode"] != test.errorCode || d["emptyResponse"] != false {
					t.Fatalf("terminalErr=%v diagnostics=%#v", terminalErr, d)
				}
				if strings.Count(logs, `"category":"llm_model_attempt_error"`) != 1 {
					t.Fatalf("expected one attempt error log: %s", logs)
				}
			} else if terminalErr != nil {
				t.Fatal(terminalErr)
			}
			if trace["status"] != wantStatus {
				t.Fatalf("trace status=%v want %s", trace["status"], wantStatus)
			}
			if test.status == 200 {
				transport := d["stream"].(map[string]any)["response"].(map[string]any)
				if transport["httpStatus"] != float64(200) || transport["upstreamRequestId"] != "upstream-123" {
					t.Fatalf("transport=%#v", transport)
				}
			}
			data, _ := json.Marshal(d)
			for _, secret := range []string{"private-prompt", "private-thinking", "private-refusal", "private-answer", "private-cookie", "private-upstream-body"} {
				if strings.Contains(logs, secret) || strings.Contains(string(data), secret) {
					t.Fatalf("diagnostic leaked %s", secret)
				}
			}
		})
	}
}

func TestEmptyResponseObservationWithoutTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	engine := newTraceTestEngine(t, t.TempDir(), server.URL, nil)
	engine.cfg.Logging.LLMInteraction.RecordEnabled = false
	req := api.QueryRequest{ChatID: "chat_1", Message: "hi"}
	logs := captureStandardLog(t, func() {
		session := traceTestSession()
		session.ResolvedBudget.Model.RetryCount = 2
		stream, err := engine.newRunStream(context.Background(), req, traceTestSessionWithSystemCache(t, engine, req, session), false)
		if err != nil {
			t.Fatal(err)
		}
		drainTraceTestStream(t, stream)
	})
	if strings.Count(logs, `"category":"llm_empty_response"`) != 1 || !strings.Contains(logs, `"doneSeen":true`) {
		t.Fatalf("missing anomaly without debug logging: %s", logs)
	}
}

func TestEmptyResponseObservationStreamEnd(t *testing.T) {
	for _, test := range []struct {
		name, trigger string
		body          func() io.ReadCloser
	}{
		{"done", "done_marker", func() io.ReadCloser { return io.NopCloser(strings.NewReader("data: [DONE]\n\n")) }},
		{"eof", "eof_after_finish", func() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }},
		{"timeout", "trailing_timeout", func() io.ReadCloser { return newBlockingReadCloser() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := &LLMAgentEngine{}
			protocol := &openAIProtocol{engine: engine}
			stream := newOpenAITerminationTestStream(engine, protocol, qwenStyleStreamEndCompat(10), test.body())
			stream.currentTurn.content.Reset()
			stream.currentTurn.finishReason = "stop"
			logs := captureStandardLog(t, func() {
				done, err := stream.consumeCurrentTurn()
				if err != nil || !done {
					t.Fatalf("done=%v err=%v", done, err)
				}
			})
			if !strings.Contains(logs, `"completionTrigger":"`+test.trigger+`"`) || !strings.Contains(logs, `"category":"llm_empty_response"`) {
				t.Fatalf("unexpected diagnostics: %s", logs)
			}
		})
	}
}

func TestEmptyResponseObservationAnthropic(t *testing.T) {
	engine := &LLMAgentEngine{}
	body := io.NopCloser(strings.NewReader("event: content_block_delta\ndata: {\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"private-thinking\"}}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\n"))
	stream := newOpenAITerminationTestStream(engine, &openAIProtocol{engine: engine}, protocolRuntimeConfig{}, body)
	stream.model.Protocol = "ANTHROPIC"
	stream.protocol = &anthropicProtocol{engine: engine}
	stream.currentTurn.content.Reset()
	logs := captureStandardLog(t, func() {
		for stream.currentTurn != nil {
			if _, err := stream.consumeCurrentTurn(); err != nil {
				t.Fatal(err)
			}
		}
	})
	if !strings.Contains(logs, `"reason":"output_limit"`) || !strings.Contains(logs, `"completionTrigger":"message_delta_stop_reason"`) || !strings.Contains(logs, `"rawReasoningBytes":16`) || strings.Contains(logs, "private-thinking") {
		t.Fatalf("unexpected diagnostics: %s", logs)
	}
}
