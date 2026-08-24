package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestActiveRunManualCompactWaitsForCurrentTurnAndPersistsBeforeCompletion(t *testing.T) {
	const chatID = "chat-active-blocking-compact"
	draft := strings.Repeat("draft before compact ", 2048)
	var calls atomic.Int32
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(release)
	firstStarted := make(chan struct{})
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeProviderSSE(t, w, fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, draft))
			close(firstStarted)
			<-releaseFirst
			writeProviderSSE(t, w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
		case 2:
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"blocking compact summary"},"finish_reason":"stop"}]}`, `[DONE]`)
		default:
			t.Errorf("unexpected provider call %d", calls.Load())
			writeProviderSSE(t, w, `[DONE]`)
		}
	})
	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	queryResp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"chatId":"`+chatID+`","message":"keep this current instruction"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer queryResp.Body.Close()
	reader := bufio.NewReader(queryResp.Body)
	for {
		line, readErr := reader.ReadString('\n')
		if strings.Contains(line, "draft before compact") {
			break
		}
		if readErr != nil {
			t.Fatalf("read stream before compact: %v", readErr)
		}
	}
	<-firstStarted

	type compactHTTPResult struct {
		response api.ApiResponse[api.CompactResponse]
		err      error
	}
	compactDone := make(chan compactHTTPResult, 1)
	go func() {
		resp, postErr := http.Post(httpServer.URL+"/api/compact", "application/json", bytes.NewBufferString(`{"chatId":"`+chatID+`","requestId":"req-blocking","trigger":"manual","level":"summary"}`))
		if postErr != nil {
			compactDone <- compactHTTPResult{err: postErr}
			return
		}
		defer resp.Body.Close()
		var decoded api.ApiResponse[api.CompactResponse]
		decodeErr := json.NewDecoder(resp.Body).Decode(&decoded)
		compactDone <- compactHTTPResult{response: decoded, err: decodeErr}
	}()
	select {
	case result := <-compactDone:
		t.Fatalf("compact returned before current turn reached a safe point: %#v err=%v", result.response, result.err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	result := <-compactDone
	if result.err != nil {
		t.Fatalf("compact request: %v", result.err)
	}
	if !result.response.Data.Accepted || result.response.Data.Status != "completed" || result.response.Data.Scope != "run" || result.response.Data.SummarySource != "model" {
		t.Fatalf("blocking compact response = %#v", result.response.Data)
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatalf("drain query: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want original + compact summary", calls.Load())
	}
	detail, err := fixture.chats.LoadChat(chatID)
	if err != nil {
		t.Fatalf("LoadChat: %v", err)
	}
	compactIndex, completeIndex := -1, -1
	for index, event := range detail.Events {
		switch event.Type {
		case "context.compact.complete":
			compactIndex = index
		case "run.complete":
			completeIndex = index
		}
	}
	if compactIndex < 0 || completeIndex < 0 || compactIndex >= completeIndex {
		t.Fatalf("event order compact=%d complete=%d", compactIndex, completeIndex)
	}
	raw, err := fixture.chats.LoadRawMessages(chatID, chat.DefaultHistoryRunWindow)
	if err != nil {
		t.Fatalf("LoadRawMessages: %v", err)
	}
	if len(raw) < 2 || !strings.Contains(fmt.Sprint(raw[0]["content"]), "blocking compact summary") {
		t.Fatalf("checkpoint messages were not restored: %#v", raw)
	}
}

func TestHandleCompactRoutesActiveRunAndBlocksForPersistedResult(t *testing.T) {
	fixture := newTestFixture(t)
	chatID := "chat-active-compact"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "active compact"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	_, control, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{
		RunID: "run-active-compact", ChatID: chatID, RunScopeID: chatID,
	})
	defer fixture.runs.Finish("run-active-compact")
	control.EnableContextCompact()

	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fixture.server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/compact", bytes.NewBufferString(`{"chatId":"`+chatID+`","requestId":"req-active","trigger":"manual","level":"summary"}`)))
	}()
	deadline := time.Now().Add(time.Second)
	for !control.HasPendingCompact() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	request, ok := control.ClaimCompact()
	if !ok || request.RequestID != "req-active" {
		t.Fatalf("active compact was not queued: %#v ok=%v", request, ok)
	}
	select {
	case <-done:
		t.Fatal("compact handler returned before the active run completed the request")
	default:
	}
	control.CompleteCompact(request.RequestID, api.CompactResponse{
		Accepted: true, Status: "completed", RequestID: request.RequestID, ChatID: chatID,
		RunID: "run-active-compact", CompactID: request.CompactID, Trigger: "manual", Scope: "run", Level: "summary",
		PostCompactEstimatedTokens: 1234,
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("compact handler did not unblock")
	}
	var response api.ApiResponse[api.CompactResponse]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Data.Accepted || response.Data.Scope != "run" || response.Data.RunID != "run-active-compact" || response.Data.PostCompactEstimatedTokens != 1234 {
		t.Fatalf("active compact response = %#v", response.Data)
	}
}

func TestHandleCompactWritesCheckpointAndReloadsRawMessages(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"compact model "}}]}`,
			`{"choices":[{"delta":{"content":"summary"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	chatID := "chat-api-compact"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "first compact message"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	appendServerCompactRun(t, fixture.chats, chatID, "r1", "user r1", "assistant r1")
	appendServerCompactRun(t, fixture.chats, chatID, "r2", "user r2", "assistant r2")
	appendServerCompactRun(t, fixture.chats, chatID, "r3", "user r3", "assistant r3")
	appendServerCompactRun(t, fixture.chats, chatID, "r4", "user r4", "assistant r4")

	body := bytes.NewBufferString(`{"chatId":"` + chatID + `","agentKey":"mock-agent","requestId":"req-compact","trigger":"manual"}`)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/compact", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.CompactResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || !response.Data.Accepted || response.Data.Status != "completed" {
		t.Fatalf("unexpected compact response: %#v", response)
	}
	if response.Data.SummarySource != "model" {
		t.Fatalf("summarySource = %q, want model", response.Data.SummarySource)
	}
	if response.Data.Level != "summary" {
		t.Fatalf("level = %q, want summary", response.Data.Level)
	}

	raw, err := fixture.chats.LoadRawMessages(chatID, 1)
	if err != nil {
		t.Fatalf("LoadRawMessages: %v", err)
	}
	if len(raw) != 5 {
		t.Fatalf("raw len = %d, want summary + two tail runs", len(raw))
	}
	firstContent, _ := raw[0]["content"].(string)
	if !strings.Contains(firstContent, "compact model summary") {
		t.Fatalf("first raw content = %q", firstContent)
	}
	for _, msg := range raw {
		content, _ := msg["content"].(string)
		if strings.Contains(content, "r1") || strings.Contains(content, "r2") {
			t.Fatalf("compacted content leaked into raw messages: %#v", msg)
		}
	}
}

func TestHandleCompactSummaryEmptyFailsWithoutCheckpoint(t *testing.T) {
	var calls atomic.Int32
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeProviderSSE(t, w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	chatID := "chat-api-compact-empty-summary"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "empty summary"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	appendServerCompactRun(t, fixture.chats, chatID, "r1", "early anchor", strings.Repeat("history ", 500))

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"chatId":"` + chatID + `","agentKey":"mock-agent","requestId":"req-empty","trigger":"manual","level":"summary"}`)
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/compact", body))
	var response api.ApiResponse[api.CompactResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if response.Data.Accepted || response.Data.Status != "failed" || response.Data.Detail != "summary_empty" || !response.Data.Retryable {
		t.Fatalf("empty summary response = %#v", response.Data)
	}
	if calls.Load() != 1 {
		t.Fatalf("summary model calls = %d, want exactly one", calls.Load())
	}
	detail, err := fixture.chats.LoadChat(chatID)
	if err != nil {
		t.Fatalf("LoadChat: %v", err)
	}
	for _, event := range detail.Events {
		if event.Type == "context.compact.complete" {
			t.Fatalf("failed summary persisted a completion event: %#v", event)
		}
	}
}

func TestHandleCompactLevelL1ToolsClearsToolResultsWithoutModel(t *testing.T) {
	modelCalls := 0
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		modelCalls++
		writeProviderSSE(t, w, `[DONE]`)
	})
	chatID := "chat-api-compact-l1-tools"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "first compact message"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	for i := 1; i <= 7; i++ {
		appendServerCompactToolResult(t, fixture.chats, chatID, fmt.Sprintf("r%d", i), fmt.Sprintf("tool-%d", i), "file_read", fmt.Sprintf("file result %d %s", i, strings.Repeat("x", 2000)))
	}

	body := bytes.NewBufferString(`{"chatId":"` + chatID + `","agentKey":"mock-agent","requestId":"req-compact-l1","trigger":"manual","level":"l1_tools"}`)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/compact", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if modelCalls != 0 {
		t.Fatalf("l1_tools compact should not call model, got %d calls", modelCalls)
	}
	var response api.ApiResponse[api.CompactResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || !response.Data.Accepted || response.Data.Status != "completed" {
		t.Fatalf("unexpected compact response: %#v", response)
	}
	if response.Data.Level != "l1_tools" || response.Data.SummarySource != "" || len(response.Data.CompactionUsage) != 0 {
		t.Fatalf("unexpected l1 compact response metadata: %#v", response.Data)
	}
	if response.Data.ToolsCleared != 2 || response.Data.ToolsKept != 5 || response.Data.TokensFreed <= 0 {
		t.Fatalf("unexpected l1 compact stats: %#v", response.Data)
	}

	raw, err := fixture.chats.LoadRawMessages(chatID, 20)
	if err != nil {
		t.Fatalf("LoadRawMessages: %v", err)
	}
	toolContent := map[string]string{}
	for _, msg := range raw {
		if stringValue(msg["role"]) == "tool" {
			toolContent[stringValue(msg["tool_call_id"])] = stringValue(msg["content"])
		}
	}
	if !strings.HasPrefix(toolContent["tool-1"], "[Compacted tool interaction]") || !strings.HasPrefix(toolContent["tool-2"], "[Compacted tool interaction]") {
		t.Fatalf("old tool results were not structurally compacted: %#v", toolContent)
	}
	if !strings.Contains(toolContent["tool-7"], "file result 7") {
		t.Fatalf("recent tool result should be kept, got %q", toolContent["tool-7"])
	}
}

func TestHandleCompactRejectsInvalidLevel(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	})
	chatID := "chat-api-compact-invalid-level"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "first compact message"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}

	body := bytes.NewBufferString(`{"chatId":"` + chatID + `","level":"bogus"}`)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/compact", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid compact level") {
		t.Fatalf("expected invalid level response, got %s", rec.Body.String())
	}
}

func TestWSCompactWritesCheckpointAndReloadsRawMessages(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"compact ws "}}]}`,
			`{"choices":[{"delta":{"content":"summary"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{notifications: ws.NewHub()})
	chatID := "chat-ws-compact"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "first compact message"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	appendServerCompactRun(t, fixture.chats, chatID, "r1", "user r1", "assistant r1")
	appendServerCompactRun(t, fixture.chats, chatID, "r2", "user r2", "assistant r2")
	appendServerCompactRun(t, fixture.chats, chatID, "r3", "user r3", "assistant r3")
	appendServerCompactRun(t, fixture.chats, chatID, "r4", "user r4", "assistant r4")

	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/compact",
		ID:    "compact_ws",
		Payload: ws.MarshalPayload(map[string]any{
			"requestId": "req-ws-compact",
			"chatId":    chatID,
			"agentKey":  "mock-agent",
			"trigger":   "manual",
		}),
	}); err != nil {
		t.Fatalf("write compact ws request: %v", err)
	}
	response := waitForWebSocketResponseData[api.CompactResponse](t, conn, "compact_ws")
	if !response.Accepted || response.Status != "completed" || response.ChatID != chatID {
		t.Fatalf("unexpected compact websocket response: %#v", response)
	}
	if response.SummarySource != "model" {
		t.Fatalf("summarySource = %q, want model", response.SummarySource)
	}
	if response.Level != "summary" {
		t.Fatalf("level = %q, want summary", response.Level)
	}

	raw, err := fixture.chats.LoadRawMessages(chatID, 1)
	if err != nil {
		t.Fatalf("LoadRawMessages: %v", err)
	}
	if len(raw) != 5 {
		t.Fatalf("raw len = %d, want summary + two tail runs", len(raw))
	}
	firstContent, _ := raw[0]["content"].(string)
	if !strings.Contains(firstContent, "compact ws summary") {
		t.Fatalf("first raw content = %q", firstContent)
	}
}

func TestWSCompactLevelL1Tools(t *testing.T) {
	modelCalls := 0
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		modelCalls++
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: ws.NewHub()})
	chatID := "chat-ws-compact-l1-tools"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "first compact message"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	for i := 1; i <= 7; i++ {
		appendServerCompactToolResult(t, fixture.chats, chatID, fmt.Sprintf("r%d", i), fmt.Sprintf("tool-%d", i), "bash", fmt.Sprintf("bash result %d %s", i, strings.Repeat("x", 2000)))
	}

	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/compact",
		ID:    "compact_ws_l1",
		Payload: ws.MarshalPayload(map[string]any{
			"requestId": "req-ws-compact-l1",
			"chatId":    chatID,
			"agentKey":  "mock-agent",
			"trigger":   "manual",
			"level":     "l1_tools",
		}),
	}); err != nil {
		t.Fatalf("write compact ws request: %v", err)
	}
	response := waitForWebSocketResponseData[api.CompactResponse](t, conn, "compact_ws_l1")
	if !response.Accepted || response.Status != "completed" || response.ChatID != chatID {
		t.Fatalf("unexpected compact websocket response: %#v", response)
	}
	if response.Level != "l1_tools" || response.ToolsCleared != 2 || response.ToolsKept != 5 || response.SummarySource != "" {
		t.Fatalf("unexpected l1 compact websocket response: %#v", response)
	}
	if modelCalls != 0 {
		t.Fatalf("l1_tools compact should not call model, got %d calls", modelCalls)
	}
}

func TestWSCompactRejectsMissingChatID(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: ws.NewHub()})
	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/compact",
		ID:      "compact_missing_chat",
		Payload: ws.MarshalPayload(map[string]any{"requestId": "req-missing-chat"}),
	}); err != nil {
		t.Fatalf("write compact ws request: %v", err)
	}
	raw := waitForWebSocketFrame(t, conn, func(raw []byte) bool {
		var frame ws.ErrorFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			return false
		}
		return frame.Frame == ws.FrameError && frame.ID == "compact_missing_chat"
	})
	var frame ws.ErrorFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode compact ws error: %v", err)
	}
	if frame.Type != "invalid_request" || frame.Code != http.StatusBadRequest || strings.Contains(frame.Msg, "unknown type") {
		t.Fatalf("unexpected compact ws error: %#v", frame)
	}
}

func appendServerCompactRun(t *testing.T, store chat.Store, chatID string, runID string, userText string, assistantText string) {
	t.Helper()
	startedAt := testEpochMillis + 100
	assistantAt := startedAt + 1
	startServerFixtureRun(t, store, chatID, runID, startedAt)
	if err := store.AppendQueryLine(chatID, chat.QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: startedAt,
		Query:     map[string]any{"role": "user", "message": userText},
		Messages:  []map[string]any{{"role": "user", "content": userText, "ts": startedAt}},
	}); err != nil {
		t.Fatalf("AppendQueryLine(%s): %v", runID, err)
	}
	if err := store.AppendStepLine(chatID, chat.StepLine{
		Type:      chat.StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: assistantAt,
		Messages: []chat.StoredMessage{
			{
				Role:    "assistant",
				Content: []chat.ContentPart{{Type: "text", Text: assistantText}},
				Ts:      &assistantAt,
			},
		},
	}); err != nil {
		t.Fatalf("AppendStepLine(%s): %v", runID, err)
	}
}

func appendServerCompactToolResult(t *testing.T, store chat.Store, chatID string, runID string, toolID string, toolName string, resultText string) {
	t.Helper()
	startedAt := testEpochMillis + 200
	messageAt := startedAt + 1
	startServerFixtureRun(t, store, chatID, runID, startedAt)
	if err := store.AppendStepLine(chatID, chat.StepLine{
		Type:      chat.StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: messageAt,
		Messages: []chat.StoredMessage{
			{
				Role: "assistant",
				ToolCalls: []chat.StoredToolCall{{
					ID:   toolID,
					Type: "function",
					Function: chat.StoredFunction{
						Name:      toolName,
						Arguments: "{}",
					},
				}},
				Ts: &messageAt,
			},
			{
				Role:       "tool",
				Name:       toolName,
				ToolCallID: toolID,
				Content:    []chat.ContentPart{{Type: "text", Text: resultText}},
				Ts:         &messageAt,
			},
		},
	}); err != nil {
		t.Fatalf("AppendStepLine(%s): %v", runID, err)
	}
}
