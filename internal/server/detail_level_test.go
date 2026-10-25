package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/stream"
	platformws "agent-platform/internal/ws"
	"bytes"
	"context"
	"encoding/json"
	gws "github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newDatetimeToolLoopFixture(t *testing.T) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
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
	}, testFixtureOptions{notifications: platformws.NewHub()})
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
	want := []string{"正在思考", "正在调用「日期时间」", "正在思考", ""}
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
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "detail must be summary, full or none") {
			t.Fatalf("%s: %d %s", req.URL, rec.Code, rec.Body.String())
		}
	}
	// A Run's level is fixed by its query; attach cannot pass one, valid or not.
	for _, detail := range []string{"full", ""} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/attach?runId=x&detail="+detail, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "detail_not_allowed") {
			t.Fatalf("attach detail=%q: %d %s", detail, rec.Code, rec.Body.String())
		}
	}
}

func TestConfiguredAgentDetailAcrossHTTPAndWS(t *testing.T) {
	f := newDatetimeToolLoopFixture(t)
	path := filepath.Join(f.cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("\ninteractionConfig:\n  defaultDetail: full\n")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.registry.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		detail string
		full   bool
	}{{"", true}, {"summary", false}, {"full", true}} {
		chatID := "configured-" + tc.detail
		url := "/api/query"
		if tc.detail != "" {
			url += "?detail=" + tc.detail
		}
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"chatId":"`+chatID+`","message":"time"}`)))
		if rec.Code != 200 {
			t.Fatalf("query: %d %s", rec.Code, rec.Body.String())
		}
		sawTool := false
		runID := ""
		for _, event := range decodeSSEMessages(t, rec.Body.String()) {
			if event["type"] == "tool.start" {
				sawTool = true
			}
			if event["type"] == "run.start" {
				runID, _ = event["runId"].(string)
				if display, _ := event["display"].(map[string]any); display["defaultDetail"] != "full" {
					t.Fatal(event)
				}
			}
		}
		if sawTool != tc.full {
			t.Fatalf("override=%q tools=%v", tc.detail, sawTool)
		}
		suffix := ""
		if tc.detail != "" {
			suffix = "&detail=" + tc.detail
		}
		rec = httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID+suffix, nil))
		var response api.ApiResponse[api.ChatDetailResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		sawTool = false
		for _, event := range response.Data.Events {
			if event.Type == "tool.snapshot" {
				sawTool = true
			}
			if event.Payload["display"] != nil {
				t.Fatal("delivery metadata persisted")
			}
		}
		if sawTool != tc.full || response.Data.Display.DefaultDetail != "full" {
			t.Fatalf("history: %s", rec.Body.String())
		}
		rec = httptest.NewRecorder()
		// Attach follows the level the Run was started with.
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/attach?agentKey=mock-agent&runId="+runID, nil))
		if rec.Code != 200 {
			t.Fatalf("attach: %d %s", rec.Code, rec.Body.String())
		}
		sawTool = false
		for _, event := range decodeSSEMessages(t, rec.Body.String()) {
			if event["type"] == "tool.start" {
				sawTool = true
			}
		}
		if sawTool != tc.full {
			t.Fatalf("attach override=%q tools=%v", tc.detail, sawTool)
		}
	}
	srv := newLoopbackServer(t, f.server)
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	for _, tc := range []struct {
		detail string
		full   bool
	}{{"", true}, {"summary", false}} {
		id := "ws-" + tc.detail
		frame := map[string]any{"frame": "request", "type": "/api/query", "id": id, "payload": map[string]any{"chatId": id, "message": "time"}}
		if tc.detail != "" {
			frame["detail"] = tc.detail
		}
		if err := conn.WriteJSON(frame); err != nil {
			t.Fatal(err)
		}
		sawTool, sawDisplay := false, false
		for {
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var message struct {
				Frame, ID, Type string
				Event           map[string]any
				Code            int
			}
			if err := conn.ReadJSON(&message); err != nil {
				t.Fatal(err)
			}
			if message.ID != id {
				continue
			}
			if message.Frame == "error" {
				t.Fatalf("WS error: %+v", message)
			}
			if message.Event["type"] == "tool.start" {
				sawTool = true
			}
			if message.Event["type"] == "run.start" {
				display, _ := message.Event["display"].(map[string]any)
				sawDisplay = display["defaultDetail"] == "full"
			}
			if message.Frame == "stream" && message.Event == nil {
				break
			}
		}
		if sawTool != tc.full || !sawDisplay {
			t.Fatalf("WS override=%q tools=%v display=%v", tc.detail, sawTool, sawDisplay)
		}
	}
}

func TestChatReadDetailFollowsActiveRun(t *testing.T) {
	active := api.ChatDetailResponse{ActiveRun: &api.ActiveRunInfo{RunID: "run_1", Detail: stream.DetailNone}}
	if got := chatReadDetail(active, stream.DetailFull); got != stream.DetailNone {
		t.Fatalf("active Chat read at %q, want the Run's none", got)
	}
	if got := chatReadDetail(api.ChatDetailResponse{}, stream.DetailFull); got != stream.DetailFull {
		t.Fatalf("idle Chat read at %q, want the requested full", got)
	}
}
