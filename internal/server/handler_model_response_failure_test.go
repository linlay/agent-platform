package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/chat"
)

func TestQueryModelResponseFailureIsPersistedWithoutRetry(t *testing.T) {
	for _, test := range []struct{ name, frame, code, message string }{
		{"reasoning_limit", `{"choices":[{"delta":{"reasoning_content":"thinking"},"finish_reason":"length"}]}`, "model_output_limit", "仅生成了思考内容"},
		{"partial_limit", `{"choices":[{"delta":{"content":"partial answer"},"finish_reason":"length"}]}`, "model_output_limit", "回答可能不完整"},
		{"truncated_tool", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{"}}]},"finish_reason":"length"}]}`, "model_output_limit", "任务未完成"},
		{"anthropic_limit", `{"choices":[{"delta":{},"finish_reason":"max_tokens"}]}`, "model_output_limit", "任务未完成"},
		{"empty", `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "model_empty_response", "没有返回回答或工具调用"},
		{"reasoning_only", `{"choices":[{"delta":{"reasoning_content":"thinking"},"finish_reason":"stop"}]}`, "model_empty_response", "仅返回了思考内容"},
		{"filtered", `{"choices":[{"delta":{},"finish_reason":"content_filter"}]}`, "provider_content_filter", "内容过滤或拒绝"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				writeProviderSSE(t, w, test.frame)
			})
			recorder := httptest.NewRecorder()
			fixture.server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"response-failure","message":"hello"}`)))
			body := recorder.Body.String()
			if calls.Load() != 1 || !strings.Contains(body, `"code":"`+test.code+`"`) || !strings.Contains(body, test.message) || !strings.Contains(body, `"type":"run.error"`) || strings.Contains(body, `"type":"run.complete"`) || strings.Contains(body, `"type":"tool.result"`) {
				t.Fatalf("calls=%d unexpected stream: %s", calls.Load(), body)
			}
			detail, err := fixture.chats.LoadChat("response-failure")
			if err != nil {
				t.Fatal(err)
			}
			persisted := false
			for _, event := range detail.Events {
				payload, _ := json.Marshal(event.Payload)
				if event.Type == "run.error" && strings.Contains(string(payload), test.code) {
					persisted = true
				}
			}
			if !persisted {
				t.Fatal("failure was not persisted")
			}
			if test.name == "truncated_tool" {
				raw, err := fixture.chats.LoadRawMessages("response-failure", chat.DefaultHistoryRunWindow)
				if err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(raw)
				if strings.Contains(string(encoded), "call_1") {
					t.Fatal("truncated tool entered continuation history")
				}
			}
			if test.name == "partial_limit" && !strings.Contains(body, "partial answer") {
				t.Fatal("partial answer lost")
			}
		})
	}
}
