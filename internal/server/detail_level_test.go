package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newDatetimeToolLoopFixture(t *testing.T) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		for _, item := range messages {
			if message, _ := item.(map[string]any); message["role"] == "tool" {
				writeProviderSSE(t, w,
					`{"choices":[{"delta":{"content":"现在是下午"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`,
					`[DONE]`,
				)
				return
			}
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_datetime","type":"function","function":{"name":"datetime","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`,
			`[DONE]`,
		)
	})
}

func TestQueryDefaultsToSummaryDetail(t *testing.T) {
	fixture := newDatetimeToolLoopFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"现在几点？"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "zh-CN")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var activities []string
	var lastSeq float64
	var complete map[string]any
	for _, message := range decodeSSEMessages(t, rec.Body.String()) {
		eventType, _ := message["type"].(string)
		switch {
		case strings.HasPrefix(eventType, "tool."), strings.HasPrefix(eventType, "reasoning."),
			eventType == "usage.snapshot", eventType == "debug.llmChat":
			t.Fatalf("summary leaked %s: %#v", eventType, message)
		case eventType == "run.activity":
			if message["seq"] != float64(0) || message["activity"] != nil || message["retry"] != nil || message["phase"] != nil {
				t.Fatalf("unexpected summary activity %#v", message)
			}
			activities = append(activities, message["message"].(string))
			continue
		case eventType == "run.complete":
			complete = message
		}
		seq, _ := message["seq"].(float64)
		if seq <= lastSeq {
			t.Fatalf("non-increasing seq at %#v", message)
		}
		lastSeq = seq
	}
	// Content clears the line with an empty message instead of "replying".
	want := []string{"正在思考", "正在准备「日期时间」", "正在调用「日期时间」", "正在思考", ""}
	if strings.Join(activities, "|") != strings.Join(want, "|") {
		t.Fatalf("activities=%q want %q", activities, want)
	}
	usage, _ := complete["usage"].(map[string]any)
	run, _ := usage["run"].(map[string]any)
	// Summary viewers read usage from the terminal event.
	if run["toolCallCount"] != float64(1) || run["llmChatCompletionCount"] != float64(2) ||
		run["promptTokens"] != float64(30) || run["completionTokens"] != float64(7) {
		t.Fatalf("unexpected terminal usage: %#v", complete)
	}
}

func TestChatHistoryHonorsDetailLevel(t *testing.T) {
	fixture := newDatetimeToolLoopFixture(t)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query?detail=full", bytes.NewBufferString(`{"chatId":"chat-detail","message":"现在几点？"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("query: %d %s", rec.Code, rec.Body.String())
	}
	types := func(detail string) []string {
		path := "/api/chat?chatId=chat-detail"
		if detail != "" {
			path += "&detail=" + detail
		}
		chatRec := httptest.NewRecorder()
		fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, path, nil))
		if chatRec.Code != http.StatusOK {
			t.Fatalf("chat %s: %d %s", detail, chatRec.Code, chatRec.Body.String())
		}
		var response struct {
			Data struct {
				Events []map[string]any `json:"events"`
				Runs   []map[string]any `json:"runs"`
			} `json:"data"`
		}
		if err := json.Unmarshal(chatRec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Data.Runs) == 0 || response.Data.Runs[0]["usage"] == nil {
			t.Fatalf("history lost per-run usage: %s", chatRec.Body.String())
		}
		out := make([]string, 0, len(response.Data.Events))
		for _, event := range response.Data.Events {
			out = append(out, event["type"].(string))
		}
		return out
	}
	summary := types("")
	assertStringSliceContains(t, summary, "request.query", "run.start", "content.snapshot")
	assertStringSliceExcludes(t, summary, "tool.snapshot", "usage.snapshot")
	assertStringSliceContains(t, types("full"), "tool.snapshot")
}

func TestDetailLevelRejectsUnknownValue(t *testing.T) {
	fixture := newDatetimeToolLoopFixture(t)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/query?detail=verbose", bytes.NewBufferString(`{"message":"hi"}`)),
		httptest.NewRequest(http.MethodGet, "/api/chat?chatId=x&detail=verbose", nil),
		httptest.NewRequest(http.MethodGet, "/api/attach?runId=x&detail=verbose", nil),
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "detail must be summary or full") {
			t.Fatalf("%s: %d %s", req.URL, rec.Code, rec.Body.String())
		}
	}
}
