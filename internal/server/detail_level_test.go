package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/config"
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

// newDatetimeToolLoopFixture runs one datetime tool loop. level is every
// mode's configured default; empty leaves it unset, which resolves to simple.
func newDatetimeToolLoopFixture(t *testing.T, level string) testFixture {
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
	}, testFixtureOptions{notifications: platformws.NewHub(), configure: func(cfg *config.Config) {
		cfg.ModeDefaultDetails = allModesDetail(level)
	}})
}

func TestQueryDefaultsToSimpleDetail(t *testing.T) {
	fixture := newDatetimeToolLoopFixture(t, "")
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"现在几点？"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "zh-CN")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var activities []string
	var usageSnapshots int
	var lastSeq float64
	var complete map[string]any
	for _, message := range decodeSSEMessages(t, rec.Body.String()) {
		eventType, _ := message["type"].(string)
		switch {
		case strings.HasPrefix(eventType, "tool."), strings.HasPrefix(eventType, "reasoning."),
			eventType == "debug.llmChat":
			t.Fatalf("simple leaked %s: %#v", eventType, message)
		case eventType == "usage.snapshot":
			usageSnapshots++
		case eventType == "run.activity":
			if message["seq"] != float64(0) || message["activity"] != nil || message["phase"] != nil {
				t.Fatalf("unexpected simple activity %#v", message)
			}
			if message["kind"] != nil || message["initial"] != nil {
				t.Fatalf("activity leaked internal metadata %#v", message)
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
	// Automatic progress is hidden; content still clears the activity line.
	if len(activities) != 2 || activities[0] == "" || activities[len(activities)-1] != "" {
		t.Fatalf("activities=%q", activities)
	}
	if usageSnapshots == 0 {
		t.Fatal("simple did not receive live usage")
	}
	usage, _ := complete["usage"].(map[string]any)
	run, _ := usage["run"].(map[string]any)
	// Terminal usage remains available alongside live snapshots.
	if run["toolCallCount"] != float64(1) || run["llmChatCompletionCount"] != float64(2) ||
		run["promptTokens"] != float64(30) || run["completionTokens"] != float64(7) {
		t.Fatalf("unexpected terminal usage: %#v", complete)
	}
}

func TestChatHistoryHonorsDetailLevel(t *testing.T) {
	fixture := newDatetimeToolLoopFixture(t, "")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"chat-detail","message":"现在几点？"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("query: %d %s", rec.Code, rec.Body.String())
	}
	// Delivery can finish just before the runtime clears its active Run. Read
	// history only after that boundary; an active Run overrides read detail.
	for _, event := range decodeSSEMessages(t, rec.Body.String()) {
		if event["type"] == "run.start" {
			waitRunTerminal(t, fixture.server, event["runId"].(string))
		}
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
	simple := types("")
	assertStringSliceContains(t, simple, "request.query", "run.start", "content.snapshot")
	assertStringSliceExcludes(t, simple, "tool.snapshot", "usage.snapshot")
	assertStringSliceContains(t, types("detailed"), "tool.snapshot")
}

// A Run is delivered at its Agent's configured level: requests that start or
// observe one cannot name a level, valid or not. Only history reads choose one.
func TestDetailRequestRules(t *testing.T) {
	fixture := newDatetimeToolLoopFixture(t, "")
	for _, detail := range []string{"detailed", "simple", "verbose", ""} {
		for _, req := range []*http.Request{
			httptest.NewRequest(http.MethodPost, "/api/query?detail="+detail, bytes.NewBufferString(`{"message":"hi"}`)),
			httptest.NewRequest(http.MethodPost, "/api/send?detail="+detail, bytes.NewBufferString(`{"requestId":"r","message":"hi"}`)),
		} {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "detail_not_allowed") {
				t.Fatalf("%s: %d %s", req.URL, rec.Code, rec.Body.String())
			}
		}
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/attach?runId=x&detail="+detail, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "detail_not_allowed") {
			t.Fatalf("attach detail=%q: %d %s", detail, rec.Code, rec.Body.String())
		}
	}
	for _, detail := range []string{"verbose", "none"} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId=x&detail="+detail, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "detail must be simple or detailed") {
			t.Fatalf("chat detail=%q: %d %s", detail, rec.Code, rec.Body.String())
		}
	}
}

func TestConfiguredAgentDetailAcrossHTTPAndWS(t *testing.T) {
	f := newDatetimeToolLoopFixture(t, "")
	path := filepath.Join(f.cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("\ninteractionConfig:\n  defaultDetail: detailed\n")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.registry.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	chatID := "configured"
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"chatId":"`+chatID+`","message":"time"}`)))
	if rec.Code != 200 {
		t.Fatalf("query: %d %s", rec.Code, rec.Body.String())
	}
	sawTool := false
	runID := ""
	for _, event := range decodeSSEMessages(t, rec.Body.String()) {
		if event["type"] == "run.activity" && event["kind"] != nil {
			t.Fatalf("detailed received simulated thinking: %#v", event)
		}
		if event["type"] == "tool.start" {
			sawTool = true
		}
		if event["type"] == "run.start" {
			runID, _ = event["runId"].(string)
			if display, _ := event["display"].(map[string]any); display["defaultDetail"] != "detailed" || display["effectiveDetail"] != "detailed" {
				t.Fatal(event)
			}
		}
	}
	if !sawTool {
		t.Fatal("configured detailed Agent hid tool calls")
	}
	waitRunTerminal(t, f.server, runID)
	// History reads at the Agent default unless the reader names a level.
	for _, tc := range []struct {
		detail   string
		detailed bool
	}{{"", true}, {"simple", false}, {"detailed", true}} {
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
		if sawTool != tc.detailed || response.Data.Display.DefaultDetail != "detailed" {
			t.Fatalf("history read=%q: %s", tc.detail, rec.Body.String())
		}
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
	if !sawTool {
		t.Fatal("attach hid tool calls of a detailed Run")
	}
	srv := newLoopbackServer(t, f.server)
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	for _, tc := range []struct {
		detail   string
		rejected bool
	}{{"", false}, {"simple", true}} {
		id := "ws-" + tc.detail
		frame := map[string]any{"frame": "request", "type": "/api/query", "id": id, "payload": map[string]any{"chatId": id, "message": "time"}}
		if tc.detail != "" {
			frame["detail"] = tc.detail
		}
		if err := conn.WriteJSON(frame); err != nil {
			t.Fatal(err)
		}
		sawTool, sawDisplay, rejected := false, false, false
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
				rejected = message.Type == "invalid_detail"
				break
			}
			if message.Event["type"] == "tool.start" {
				sawTool = true
			}
			if message.Event["type"] == "run.start" {
				display, _ := message.Event["display"].(map[string]any)
				sawDisplay = display["defaultDetail"] == "detailed"
			}
			if message.Frame == "stream" && message.Event == nil {
				break
			}
		}
		if rejected != tc.rejected || (!tc.rejected && (!sawTool || !sawDisplay)) {
			t.Fatalf("WS detail=%q rejected=%v tools=%v display=%v", tc.detail, rejected, sawTool, sawDisplay)
		}
	}
}

// image_generate declares simpleVisible, so simple viewers keep its whole
// call while ordinary tools stay hidden.
func TestSimpleDeliveryKeepsSimpleVisibleToolCalls(t *testing.T) {
	fixture := newDatetimeToolLoopFixture(t, "")
	filter := fixture.server.detailFilter(stream.DetailSimple)
	for _, tc := range []struct {
		event stream.EventData
		want  bool
	}{
		{stream.EventData{Type: "tool.start", Payload: map[string]any{"toolId": "img", "toolName": "image_generate"}}, true},
		{stream.EventData{Type: "tool.args", Payload: map[string]any{"toolId": "img", "delta": "{}"}}, true},
		{stream.EventData{Type: "tool.start", Payload: map[string]any{"toolId": "clock", "toolName": "datetime"}}, false},
		{stream.EventData{Type: "tool.result", Payload: map[string]any{"toolId": "clock", "toolName": "datetime"}}, false},
		{stream.EventData{Type: "tool.result", Payload: map[string]any{"toolId": "img", "toolName": "image_generate"}}, true},
	} {
		if got := filter.Allows(tc.event); got != tc.want {
			t.Fatalf("%s %v: got %v", tc.event.Type, tc.event.Payload, got)
		}
	}
}

func TestChatReadDetailFollowsActiveRun(t *testing.T) {
	active := api.ChatDetailResponse{ActiveRun: &api.ActiveRunInfo{RunID: "run_1", Detail: stream.DetailSimple}}
	if got := chatReadDetail(active, stream.DetailDetailed); got != stream.DetailSimple {
		t.Fatalf("active Chat read at %q, want the Run's simple", got)
	}
	if got := chatReadDetail(api.ChatDetailResponse{}, stream.DetailDetailed); got != stream.DetailDetailed {
		t.Fatalf("idle Chat read at %q, want the requested detailed", got)
	}
}

func TestDetailedQueryShowsModelWaitingOnlyOnceAcrossToolLoop(t *testing.T) {
	fixture := newDatetimeToolLoopFixture(t, "detailed")
	req := httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"message":"time"}`))
	req.Header.Set("Accept-Language", "zh-CN")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("query: %d %s", rec.Code, rec.Body.String())
	}
	waits := 0
	for _, event := range decodeSSEMessages(t, rec.Body.String()) {
		if event["type"] != "run.activity" {
			continue
		}
		if event["kind"] != nil || event["initial"] != nil {
			t.Fatalf("activity leaked internal metadata: %#v", event)
		}
		if event["message"] != "" {
			if event["message"] != "等待模型响应" || event["seq"] != float64(0) {
				t.Fatalf("invalid wait: %#v", event)
			}
			waits++
		}
	}
	if waits != 1 {
		t.Fatalf("want exactly one initial wait across the tool loop, got %d", waits)
	}
}
