package llm

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"agent-platform/internal/contracts"
	"agent-platform/internal/modelclient"
)

func TestResponsesUpstreamErrorEnvelopes(t *testing.T) {
	for _, test := range []struct {
		name, event, raw, code, message string
	}{
		{"untyped_object", "", `{"error":{"code":"server_error","message":"no healthy upstream account","type":"upstream_error"}}`, "server_error", "no healthy upstream account"},
		{"untyped_string", "", `{"error":"no healthy upstream account"}`, "", "no healthy upstream account"},
		{"numeric_code", "", `{"error":{"code":503,"message":"service unavailable"}}`, "503", "service unavailable"},
		{"event_name_envelope", "error", `{"error":{"code":"upstream_error","message":"backend failed"}}`, "upstream_error", "backend failed"},
		{"standard_event", "", `{"type":"error","code":"server_error","message":"backend failed"}`, "server_error", "backend failed"},
		{"failed_response", "", `{"type":"response.failed","response":{"id":"resp1","status":"failed","error":{"code":"server_error","message":"backend failed"}}}`, "server_error", "backend failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, s := responseTestStream()
			s.currentTurn.observation.Response = modelclient.ResponseMetadata{StatusCode: 200, RequestID: "req-upstream"}
			_, err := p.ConsumeChunk(s, test.event, test.raw)
			payload := modelErrorPayload(err)
			if payload["code"] != "provider_stream_failed" || payload["status"] != 502 || payload["retryable"] != true || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("unexpected error: %#v", payload)
			}
			details := payload["diagnostics"].(map[string]any)
			if details["upstreamMessage"] != test.message || details["upstreamStatus"] != 200 || details["upstreamRequestId"] != "req-upstream" {
				t.Fatalf("lost upstream error or invented HTTP status: %#v", details)
			}
			if test.code != "" && details["upstreamCode"] != test.code {
				t.Fatalf("lost code: %#v", details)
			}
			if details["attempt"] != 1 || details["maxAttempts"] != 1 {
				t.Fatalf("missing attempt: %#v", details)
			}
		})
	}
}

func TestResponsesMissingTypeKeepsOnlyFrameShape(t *testing.T) {
	for _, raw := range []string{
		`{"choices":[{"delta":{"content":"private-answer"}}],"private-key-name":"private-value"}`,
		`{"error":null}`, `{"error":{}}`, `{}`, `null`,
	} {
		p, s := responseTestStream()
		_, err := p.ConsumeChunk(s, "", raw)
		payload := modelErrorPayload(err)
		if payload["code"] != "provider_stream_invalid" || err.Error() != "responses event missing type" {
			t.Fatalf("unexpected error: %#v", payload)
		}
		details := payload["diagnostics"].(map[string]any)
		if details["frameBytes"] != len(raw) || details["eventType"] != "" {
			t.Fatalf("missing frame context: %#v", details)
		}
		encoded, _ := json.Marshal(payload)
		if strings.Contains(string(encoded), "private-") {
			t.Fatalf("frame content leaked: %s", encoded)
		}
	}
	// An explicit null error must not turn a valid event into a failed request.
	p, s := responseTestStream()
	if _, err := p.ConsumeChunk(s, "response.created", `{"response":{"id":"resp1"},"error":null}`); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesOmittedOutputDiagnosticsDoNotCommit(t *testing.T) {
	for _, terminal := range []string{"completed", "incomplete"} {
		t.Run(terminal, func(t *testing.T) {
			_, s := responseTestStream()
			s.allowToolUse = true
			s.modelCall.attempt, s.modelCall.maxAttempts = 6, 6
			s.currentTurn.observation.Response.StatusCode = 200
			frames := []string{
				`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc1","call_id":"call1","name":"datetime","arguments":""}}`,
				`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc1","call_id":"call1","name":"datetime","arguments":"{}"}}`,
				`{"type":"response.` + terminal + `","response":{"id":"resp1","status":"` + terminal + `","output":[],"incomplete_details":{"reason":"max_output_tokens"}}}`,
			}
			s.currentTurn.reader = bufio.NewReader(strings.NewReader("data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"))
			for i := 0; i < 2; i++ {
				if _, err := s.consumeCurrentTurn(); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.consumeCurrentTurn()
			payload := modelErrorPayload(err)
			if payload["code"] != "provider_stream_invalid" || err.Error() != "responses terminal snapshot omitted output item" {
				t.Fatalf("unexpected error: %#v", payload)
			}
			details := payload["diagnostics"].(map[string]any)
			for key, want := range map[string]any{
				"upstreamStatus": 200, "frameNumber": 3, "eventType": "response." + terminal,
				"responseStatus": terminal, "responseId": "resp1", "incompleteReason": "max_output_tokens",
				"attempt": 6, "maxAttempts": 6, "observedItemCount": 1, "terminalOutputCount": 0,
				"outputIndex": 1, "observedItemType": "function_call", "observedItemDone": true,
			} {
				if details[key] != want {
					t.Fatalf("%s=%v want %v (%#v)", key, details[key], want, details)
				}
			}
			for _, delta := range s.pending {
				switch delta.(type) {
				case contracts.DeltaToolCall, contracts.DeltaModelTurnCommit:
					t.Fatal("invalid response committed or exposed tools")
				}
			}
			if len(s.queuedToolCalls) != 0 {
				t.Fatal("invalid response queued tools")
			}
		})
	}
}

func TestResponsesDecodeErrorHasOffset(t *testing.T) {
	p, s := responseTestStream()
	_, err := p.ConsumeChunk(s, "", `{"type":123}`)
	details := modelErrorPayload(err)["diagnostics"].(map[string]any)
	if details["jsonErrorOffset"] == nil || s.currentTurn.observation.DecodeErrors != 1 {
		t.Fatalf("missing decode detail: %#v", details)
	}
}

func TestResponsesErrorDiagnosticsPersistWithoutRawContent(t *testing.T) {
	_, s := responseTestStream()
	tracePath := filepath.Join(t.TempDir(), "trace.json")
	s.currentTurn.trace = &llmChatTrace{enabled: true, path: tracePath, payload: map[string]any{}}
	s.currentTurn.observation.Response.StatusCode = 200
	s.currentTurn.reader = bufio.NewReader(strings.NewReader("data: " + `{"error":{"code":"private-upstream-code","message":"private-upstream-message Bearer private-credential"},"input":"private-prompt"}` + "\n\n"))
	var payload map[string]any
	logs := captureStandardLog(t, func() {
		_, err := s.consumeCurrentTurn()
		payload = modelErrorPayload(err)
		if err := s.handleModelAttemptError(err); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(payload["message"].(string), "private-upstream-message") || strings.Contains(payload["message"].(string), "private-credential") {
		t.Fatalf("message lost or credential leaked: %#v", payload)
	}
	raw, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	var trace map[string]any
	if err := json.Unmarshal(raw, &trace); err != nil {
		t.Fatal(err)
	}
	diagnostics := trace["diagnostics"].(map[string]any)
	failure := diagnostics["stream"].(map[string]any)["responsesFailure"].(map[string]any)
	if failure["frameNumber"] != float64(1) || failure["upstreamStatus"] != float64(200) {
		t.Fatalf("missing persisted context: %#v", failure)
	}
	encoded, _ := json.Marshal(diagnostics)
	for _, value := range []string{"private-prompt", "private-upstream-message", "private-upstream-code", "private-credential"} {
		if strings.Contains(logs, value) || strings.Contains(string(encoded), value) {
			t.Fatalf("always-on diagnostics leaked %s", value)
		}
	}
}

func TestResponsesReportedErrorIsBounded(t *testing.T) {
	err := responsesReportedError("server_error", strings.Repeat("上游错误", 1000), "server_error")
	message := modelErrorPayload(err)["diagnostics"].(map[string]any)["upstreamMessage"].(string)
	if len(message) > 1027 || !utf8.ValidString(message) {
		t.Fatal("unbounded or invalid UTF-8 error message")
	}
}

func TestModelRetryMessageDistinguishesTimeout(t *testing.T) {
	for _, reason := range []string{"provider_timeout", "model_stream_idle_timeout", "provider_stream_invalid", "provider_stream_failed", "provider_unavailable"} {
		want := "模型响应失败，正在重试"
		if reason == "provider_timeout" || reason == "model_stream_idle_timeout" {
			want = "模型响应超时，正在重试"
		}
		if got := modelActivityMessage("retrying", reason); got != want {
			t.Fatalf("reason=%s message=%s want=%s", reason, got, want)
		}
	}
}
