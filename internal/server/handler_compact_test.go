package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

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
		appendServerCompactToolResult(t, fixture.chats, chatID, fmt.Sprintf("r%d", i), fmt.Sprintf("tool-%d", i), "file_read", fmt.Sprintf("file result %d %s", i, strings.Repeat("x", 240)))
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
	if toolContent["tool-1"] != chat.ToolCompactClearedMessage || toolContent["tool-2"] != chat.ToolCompactClearedMessage {
		t.Fatalf("old tool results were not cleared: %#v", toolContent)
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
		appendServerCompactToolResult(t, fixture.chats, chatID, fmt.Sprintf("r%d", i), fmt.Sprintf("tool-%d", i), "bash", fmt.Sprintf("bash result %d %s", i, strings.Repeat("x", 240)))
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
	if err := store.AppendQueryLine(chatID, chat.QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: 100,
		Query:     map[string]any{"role": "user", "message": userText},
	}); err != nil {
		t.Fatalf("AppendQueryLine(%s): %v", runID, err)
	}
	if err := store.AppendStepLine(chatID, chat.StepLine{
		Type:      chat.StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: 101,
		Messages: []chat.StoredMessage{
			{
				Role:    "assistant",
				Content: []chat.ContentPart{{Type: "text", Text: assistantText}},
			},
		},
	}); err != nil {
		t.Fatalf("AppendStepLine(%s): %v", runID, err)
	}
}

func appendServerCompactToolResult(t *testing.T, store chat.Store, chatID string, runID string, toolID string, toolName string, resultText string) {
	t.Helper()
	if err := store.AppendStepLine(chatID, chat.StepLine{
		Type:      chat.StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: 101,
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
			},
			{
				Role:       "tool",
				Name:       toolName,
				ToolCallID: toolID,
				Content:    []chat.ContentPart{{Type: "text", Text: resultText}},
			},
		},
	}); err != nil {
		t.Fatalf("AppendStepLine(%s): %v", runID, err)
	}
}
