package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
)

func TestSummaryCompactSingleCallBudgetAndFailure(t *testing.T) {
	for _, name := range []string{"success", "provider-error", "empty", "oversized-output"} {
		failed := name != "success"
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["tools"] != nil || body["tool_choice"] != nil {
					t.Error("summary sent tools")
				}
				if output, _ := body["max_completion_tokens"].(float64); output <= 0 || output > 4096 {
					t.Errorf("missing bounded output: %v", body["max_completion_tokens"])
				}
				if name == "provider-error" {
					http.Error(w, "temporary upstream error", http.StatusServiceUnavailable)
					return
				}
				text := "Summary with middle-anchor preserved"
				if name == "empty" {
					text = ""
				}
				if name == "oversized-output" {
					text = strings.Repeat("model summary must not be truncated ", 1000)
				}
				quoted, _ := json.Marshal(text)
				writeProviderSSE(t, w, `{"choices":[{"delta":{"content":`+string(quoted)+`},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`, "[DONE]")
			})
			id := "single-summary-" + name
			if _, _, err := fixture.chats.EnsureChat(id, "mock-agent", "", "summary"); err != nil {
				t.Fatal(err)
			}
			appendServerCompactRun(t, fixture.chats, id, "r1", "early-anchor", strings.Repeat("history ", 500)+" middle-anchor")
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/compact", bytes.NewBufferString(`{"chatId":"`+id+`","requestId":"once","level":"summary"}`)))
			var response api.ApiResponse[api.CompactResponse]
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("provider calls=%d", calls.Load())
			}
			if failed {
				wantDetail := map[string]string{"provider-error": "summary_model_failed", "empty": "summary_empty", "oversized-output": "context_window_uncompactable"}[name]
				if response.Data.Status != "failed" || response.Data.Detail != wantDetail {
					t.Fatalf("response %#v", response.Data)
				}
			} else if response.Data.Status != "completed" {
				t.Fatalf("response %#v", response.Data)
			}
			chat, err := fixture.chats.LoadChat(id)
			if err != nil {
				t.Fatal(err)
			}
			complete := 0
			for _, event := range chat.Events {
				if event.Type == "context.compact.complete" {
					complete++
				}
			}
			if (complete == 1) == failed {
				t.Fatal("wrong checkpoint count")
			}
		})
	}
}
