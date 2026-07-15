package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestHandleChatJSONLReturnsActiveRawContent(t *testing.T) {
	fixture := newTestFixture(t)
	chatID := "chat-jsonl-active"
	seedSearchableChat(t, fixture.chats, chatID)
	want, err := fixture.chats.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load raw jsonl: %v", err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/jsonl?chatId="+chatID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("content-type=%q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `inline; filename="chat-jsonl-active.jsonl"` {
		t.Fatalf("content-disposition=%q", got)
	}
	if rec.Body.String() != want {
		t.Fatalf("raw jsonl mismatch\nwant: %q\ngot:  %q", want, rec.Body.String())
	}
}

func TestHandleChatJSONLFallsBackToArchiveRawContent(t *testing.T) {
	server, active, _ := newArchiveHandlerTestServer(t, nil)
	chatID := "chat-jsonl-archive"
	seedArchiveHandlerChat(t, active, chatID)
	want, err := active.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load active raw jsonl: %v", err)
	}

	archiveRec := httptest.NewRecorder()
	server.ServeHTTP(archiveRec, httptest.NewRequest(http.MethodPost, "/api/chat/archive", strings.NewReader(`{"chatIds":["`+chatID+`"]}`)))
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive status=%d body=%s", archiveRec.Code, archiveRec.Body.String())
	}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/jsonl?chatId="+chatID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != want {
		t.Fatalf("archived raw jsonl mismatch\nwant: %q\ngot:  %q", want, rec.Body.String())
	}
}

func TestHandleChatJSONLValidationAndNotFound(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		name string
		path string
		code int
	}{
		{name: "missing", path: "/api/chat/jsonl", code: http.StatusBadRequest},
		{name: "invalid", path: "/api/chat/jsonl?chatId=../chat", code: http.StatusBadRequest},
		{name: "not found", path: "/api/chat/jsonl?chatId=missing-chat", code: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.code {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestHandleChatJSONLRejectsHistoricalTimeContractViolation(t *testing.T) {
	fixture := newTestFixture(t)
	chatID := "chat-jsonl-invalid-time"
	if _, _, err := fixture.chats.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	path := filepath.Join(fixture.cfg.Paths.ChatsDir, chatID+".jsonl")
	if err := os.WriteFile(path, []byte(`{"_type":"query","chatId":"chat-jsonl-invalid-time","runId":"run-1","updatedAt":"1700000000000","query":{}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/jsonl?chatId="+chatID, nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want=422 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "time_contract_violation") {
		t.Fatalf("expected time_contract_violation body=%s", rec.Body.String())
	}
}

func TestHandleChatSystemPromptResolvesRunSnapshot(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID = "chat-system-prompt"
	if _, _, err := fixture.chats.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	for index, snapshot := range []struct {
		fingerprint string
		content     string
	}{
		{fingerprint: "sha256:original", content: "original system prompt"},
		{fingerprint: "sha256:updated", content: "updated system prompt"},
	} {
		if err := fixture.chats.AppendQueryLine(chatID, chat.QueryLine{
			Type:      "query",
			ChatID:    chatID,
			RunID:     "run-system-prompt",
			UpdatedAt: testEpochMillis + int64(index+1),
			Query:     map[string]any{"role": "system", "kind": "system-init", "hidden": true},
			System: &chat.QueryLineSystem{
				AgentKey:      "agent-a",
				CacheKey:      "react:main",
				Fingerprint:   snapshot.fingerprint,
				SystemMessage: map[string]any{"role": "system", "content": snapshot.content},
				Tools:         []any{},
			},
		}); err != nil {
			t.Fatalf("append system init %d: %v", index, err)
		}
	}

	response := getAPIData[api.ChatSystemPromptResponse](t, fixture.server, http.MethodGet,
		"/api/chat/system-prompt?chatId=chat-system-prompt&runId=run-system-prompt&agentKey=agent-a", nil)
	if response.ChatID != chatID || response.RunID != "run-system-prompt" || response.AgentKey != "agent-a" || response.SystemRef.AgentKey != "agent-a" || response.SystemRef.CacheKey != "react:main" || response.SystemRef.Fingerprint != "sha256:original" {
		t.Fatalf("unexpected system prompt identity %#v", response)
	}
	if got, _ := response.SystemMessage["content"].(string); got != "original system prompt" {
		t.Fatalf("expected historical system prompt, got %#v", response.SystemMessage)
	}
}

func TestHandleChatSystemPromptResolvesPriorRunSnapshotFromStepRef(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID = "chat-system-prompt-reused"
	if _, _, err := fixture.chats.EnsureChat(chatID, "agent-a", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := fixture.chats.AppendQueryLine(chatID, chat.QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     "run-original",
		UpdatedAt: testEpochMillis + 1,
		Query:     map[string]any{"role": "system", "kind": "system-init", "hidden": true},
		System: &chat.QueryLineSystem{
			AgentKey:      "agent-a",
			CacheKey:      "react:main",
			Fingerprint:   "sha256:reused",
			SystemMessage: map[string]any{"role": "system", "content": "reused system prompt"},
			Tools:         []any{},
		},
	}); err != nil {
		t.Fatalf("append original system init: %v", err)
	}
	if err := fixture.chats.AppendStepLine(chatID, chat.StepLine{
		Type:      chat.StepLineTypeReact,
		ChatID:    chatID,
		RunID:     "run-reused",
		UpdatedAt: testEpochMillis + 2,
		SystemRef: map[string]any{
			"agentKey": "agent-a", "cacheKey": "react:main", "fingerprint": "sha256:reused",
		},
		Messages: []chat.StoredMessage{},
	}); err != nil {
		t.Fatalf("append reused run step: %v", err)
	}

	response := getAPIData[api.ChatSystemPromptResponse](t, fixture.server, http.MethodGet,
		"/api/chat/system-prompt?chatId=chat-system-prompt-reused&runId=run-reused&agentKey=agent-a", nil)
	if got, _ := response.SystemMessage["content"].(string); got != "reused system prompt" {
		t.Fatalf("expected reused system prompt, got %#v", response.SystemMessage)
	}
}

func TestHandleChatSystemPromptValidationAndNotFound(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		name string
		path string
		code int
	}{
		{name: "missing chat", path: "/api/chat/system-prompt", code: http.StatusBadRequest},
		{name: "invalid chat", path: "/api/chat/system-prompt?chatId=../chat&runId=run_1&agentKey=agent", code: http.StatusBadRequest},
		{name: "missing run identity", path: "/api/chat/system-prompt?chatId=chat_1", code: http.StatusBadRequest},
		{name: "chat not found", path: "/api/chat/system-prompt?chatId=missing-chat&runId=run_1&agentKey=agent", code: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.code {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestHandleChatLLMTraceRejectsFinalizedTraceWithoutRequiredTimes(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	fileParam := "chat-trace/.llm-records/run_trace_001.json"
	want := `{"runId":"run_trace","status":"ok"}` + "\n"
	seedLLMTraceFile(t, fixture, fileParam, want)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/llm-trace?file=chat-trace%2F.llm-records%2Frun_trace_001.json", nil))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "time_contract_violation") {
		t.Fatalf("expected 422 time_contract_violation, status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatLLMTraceReturnsStrictRawContent(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	fileParam := "chat-trace/.llm-records/run_trace_001.json"
	want := strictCompletedTraceContent("run_trace")
	seedLLMTraceFile(t, fixture, fileParam, want)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/llm-trace?file=chat-trace%2F.llm-records%2Frun_trace_001.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content-type=%q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `inline; filename="run_trace_001.json"` {
		t.Fatalf("content-disposition=%q", got)
	}
	if rec.Body.String() != want {
		t.Fatalf("raw llm trace mismatch\nwant: %q\ngot:  %q", want, rec.Body.String())
	}
}

func TestHandleChatLLMTraceValidationAndNotFound(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	for _, tc := range []struct {
		name string
		path string
		code int
	}{
		{name: "missing", path: "/api/chat/llm-trace", code: http.StatusBadRequest},
		{name: "path traversal", path: "/api/chat/llm-trace?file=chat_1%2F..%2Frun_001.json", code: http.StatusBadRequest},
		{name: "old global directory", path: "/api/chat/llm-trace?file=llm%2Frun_001.json", code: http.StatusBadRequest},
		{name: "wrong directory", path: "/api/chat/llm-trace?file=chat_1%2Frun_001.json", code: http.StatusBadRequest},
		{name: "wrong extension", path: "/api/chat/llm-trace?file=chat_1%2F.llm-records%2Frun_001.txt", code: http.StatusBadRequest},
		{name: "missing trace", path: "/api/chat/llm-trace?file=chat_1%2F.llm-records%2Fmissing_001.json", code: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.code {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestHandleChatLLMTraceRejectsHistoricalTimeContractViolation(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	fileParam := "chat-trace/.llm-records/run_invalid_001.json"
	seedLLMTraceFile(t, fixture, fileParam, `{"runId":"run_invalid","sentAt":"2024-01-01T00:00:00Z"}`+"\n")

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/llm-trace?file=chat-trace%2F.llm-records%2Frun_invalid_001.json", nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want=422 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "time_contract_violation") {
		t.Fatalf("expected time_contract_violation body=%s", rec.Body.String())
	}
}

func TestHandleChatLLMTraceRejectsSubMillisecondReadablePair(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	fileParam := "chat-trace/.llm-records/run_submillisecond_001.json"
	seedLLMTraceFile(t, fixture, fileParam, `{"runId":"run_submillisecond","status":"ok","sentAt":1700000000000,"sentTime":"2023-11-14T22:13:20.000000001Z","responseStartedAt":1700000000000,"responseStartedTime":"2023-11-14T22:13:20Z","completedAt":1700000000000,"completedTime":"2023-11-14T22:13:20Z"}`+"\n")

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat/llm-trace?file=chat-trace%2F.llm-records%2Frun_submillisecond_001.json", nil))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "time_contract_violation") {
		t.Fatalf("expected sub-millisecond trace pair rejection, status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestValidatePersistedJSONLTimeContractRequiresEventTimestamp(t *testing.T) {
	err := validatePersistedJSONLTimeContract(`{"_type":"event","updatedAt":1700000000000,"event":{"type":"content.delta"}}`, "chat.jsonl")
	if !timecontract.IsViolation(err) {
		t.Fatalf("expected time contract violation, got %v", err)
	}
}

func TestWSChatJSONLReturnsRawContent(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	chatID := "chat-jsonl-ws"
	seedSearchableChat(t, fixture.chats, chatID)
	want, err := fixture.chats.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load raw jsonl: %v", err)
	}
	conn := dialTestWS(t, fixture.server)
	defer conn.Close()

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/chat/jsonl",
		ID:      "req_raw_jsonl",
		Payload: ws.MarshalPayload(map[string]any{"chatId": chatID}),
	}); err != nil {
		t.Fatalf("write request: %v", err)
	}
	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if frame.Frame != ws.FrameResponse || frame.ID != "req_raw_jsonl" || frame.Code != 0 {
		t.Fatalf("unexpected response frame: %#v", frame)
	}
	data, ok := frame.Data.(string)
	if !ok {
		t.Fatalf("expected string data, got %#v", frame.Data)
	}
	if data != want {
		t.Fatalf("raw jsonl mismatch\nwant: %q\ngot:  %q", want, data)
	}
}

func TestWSChatJSONLValidationAndNotFound(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	conn := dialTestWS(t, fixture.server)
	defer conn.Close()

	for _, tc := range []struct {
		name    string
		id      string
		payload map[string]any
		code    int
		typeKey string
	}{
		{name: "missing", id: "req_missing", payload: map[string]any{}, code: http.StatusBadRequest, typeKey: "invalid_request"},
		{name: "invalid", id: "req_invalid", payload: map[string]any{"chatId": "../chat"}, code: http.StatusBadRequest, typeKey: "invalid_request"},
		{name: "not found", id: "req_not_found", payload: map[string]any{"chatId": "missing-chat"}, code: http.StatusNotFound, typeKey: "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := conn.WriteJSON(ws.RequestFrame{
				Frame:   ws.FrameRequest,
				Type:    "/api/chat/jsonl",
				ID:      tc.id,
				Payload: ws.MarshalPayload(tc.payload),
			}); err != nil {
				t.Fatalf("write request: %v", err)
			}
			var frame ws.ErrorFrame
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("read error: %v", err)
			}
			if frame.Frame != ws.FrameError || frame.ID != tc.id || frame.Type != tc.typeKey || frame.Code != tc.code {
				t.Fatalf("unexpected error frame: %#v", frame)
			}
		})
	}
}

func TestWSChatSystemPromptReturnsPersistedSnapshot(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	const chatID = "chat-system-prompt-ws"
	const runID = "run-system-prompt-ws"
	const agentKey = "agent-ws"
	if _, _, err := fixture.chats.EnsureChat(chatID, agentKey, "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := fixture.chats.AppendQueryLine(chatID, chat.QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: testEpochMillis + 1,
		Query:     map[string]any{"role": "system", "kind": "system-init", "hidden": true},
		System: &chat.QueryLineSystem{
			AgentKey:      agentKey,
			CacheKey:      "react:main",
			Fingerprint:   "sha256:ws",
			SystemMessage: map[string]any{"role": "system", "content": "persisted system prompt"},
			Tools:         []any{},
		},
	}); err != nil {
		t.Fatalf("append system init: %v", err)
	}

	conn := dialTestWS(t, fixture.server)
	defer conn.Close()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/chat/system-prompt",
		ID:      "req_system_prompt",
		Payload: ws.MarshalPayload(map[string]any{"chatId": chatID, "runId": runID, "agentKey": agentKey}),
	}); err != nil {
		t.Fatalf("write request: %v", err)
	}

	var frame ws.ResponseFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if frame.Frame != ws.FrameResponse || frame.Type != "/api/chat/system-prompt" || frame.ID != "req_system_prompt" || frame.Code != 0 {
		t.Fatalf("unexpected response frame: %#v", frame)
	}
	encoded, err := json.Marshal(frame.Data)
	if err != nil {
		t.Fatalf("marshal response data: %v", err)
	}
	var response api.ChatSystemPromptResponse
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("decode response data: %v", err)
	}
	if response.ChatID != chatID || response.RunID != runID || response.AgentKey != agentKey || response.SystemRef.AgentKey != agentKey || response.SystemRef.CacheKey != "react:main" || response.SystemRef.Fingerprint != "sha256:ws" {
		t.Fatalf("unexpected system prompt response: %#v", response)
	}
	if got, _ := response.SystemMessage["content"].(string); got != "persisted system prompt" {
		t.Fatalf("unexpected system message: %#v", response.SystemMessage)
	}
}

func TestWSChatSystemPromptValidationAndNotFound(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	const noSnapshotChatID = "chat-system-prompt-ws-no-snapshot"
	if _, _, err := fixture.chats.EnsureChat(noSnapshotChatID, "agent-ws", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	conn := dialTestWS(t, fixture.server)
	defer conn.Close()

	for _, tc := range []struct {
		name    string
		id      string
		payload json.RawMessage
		code    int
		typeKey string
	}{
		{name: "missing", id: "req_system_prompt_missing", payload: ws.MarshalPayload(map[string]any{}), code: http.StatusBadRequest, typeKey: "invalid_request"},
		{name: "malformed", id: "req_system_prompt_malformed", payload: json.RawMessage(`[]`), code: http.StatusBadRequest, typeKey: "invalid_request"},
		{name: "invalid chat", id: "req_system_prompt_invalid", payload: ws.MarshalPayload(map[string]any{"chatId": "../chat", "runId": "run_1", "agentKey": "agent"}), code: http.StatusBadRequest, typeKey: "invalid_request"},
		{name: "chat not found", id: "req_system_prompt_not_found", payload: ws.MarshalPayload(map[string]any{"chatId": "missing-chat", "runId": "run_1", "agentKey": "agent"}), code: http.StatusNotFound, typeKey: "not_found"},
		{name: "snapshot not found", id: "req_system_prompt_snapshot_not_found", payload: ws.MarshalPayload(map[string]any{"chatId": noSnapshotChatID, "runId": "run_1", "agentKey": "agent-ws"}), code: http.StatusNotFound, typeKey: "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := conn.WriteJSON(ws.RequestFrame{
				Frame:   ws.FrameRequest,
				Type:    "/api/chat/system-prompt",
				ID:      tc.id,
				Payload: tc.payload,
			}); err != nil {
				t.Fatalf("write request: %v", err)
			}
			var frame ws.ErrorFrame
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("read error: %v", err)
			}
			if frame.Frame != ws.FrameError || frame.ID != tc.id || frame.Type != tc.typeKey || frame.Code != tc.code {
				t.Fatalf("unexpected error frame: %#v", frame)
			}
		})
	}
}

func TestWSChatSystemPromptTimeContractViolation(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	const chatID = "chat-system-prompt-ws-invalid-time"
	if _, _, err := fixture.chats.EnsureChat(chatID, "agent-ws", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	path := filepath.Join(fixture.cfg.Paths.ChatsDir, chatID+".jsonl")
	content := `{"_type":"query","chatId":"` + chatID + `","runId":"run_1","updatedAt":"1700000000000","query":{},"system":{"agentKey":"agent-ws","cacheKey":"react:main","fingerprint":"sha256:invalid","systemMessage":{"role":"system","content":"invalid time"}}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	conn := dialTestWS(t, fixture.server)
	defer conn.Close()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/chat/system-prompt",
		ID:      "req_system_prompt_invalid_time",
		Payload: ws.MarshalPayload(map[string]any{"chatId": chatID, "runId": "run_1", "agentKey": "agent-ws"}),
	}); err != nil {
		t.Fatalf("write request: %v", err)
	}
	var frame ws.ErrorFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read error: %v", err)
	}
	if frame.Frame != ws.FrameError || frame.ID != "req_system_prompt_invalid_time" || frame.Code != http.StatusUnprocessableEntity || frame.Type != "time_contract_violation" {
		t.Fatalf("expected 422 time_contract_violation frame, got %#v", frame)
	}
}

func TestWSChatLLMTraceRejectsFinalizedTraceWithoutRequiredTimes(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	fileParam := "chat-trace-ws/.llm-records/run_trace_ws_001.json"
	want := `{"runId":"run_trace_ws","status":"ok"}` + "\n"
	seedLLMTraceFile(t, fixture, fileParam, want)
	conn := dialTestWS(t, fixture.server)
	defer conn.Close()

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/chat/llm-trace",
		ID:      "req_raw_llm_trace",
		Payload: ws.MarshalPayload(map[string]any{"file": fileParam}),
	}); err != nil {
		t.Fatalf("write request: %v", err)
	}
	var frame ws.ErrorFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read error: %v", err)
	}
	if frame.Frame != ws.FrameError || frame.ID != "req_raw_llm_trace" || frame.Code != http.StatusUnprocessableEntity || frame.Type != "time_contract_violation" {
		t.Fatalf("expected 422 time_contract_violation frame, got %#v", frame)
	}
}

func strictCompletedTraceContent(runID string) string {
	return `{"runId":"` + runID + `","status":"ok","sentAt":1700000000000,"sentTime":"2023-11-14T22:13:20Z","responseStartedAt":1700000000001,"responseStartedTime":"2023-11-14T22:13:20.001Z","completedAt":1700000000002,"completedTime":"2023-11-14T22:13:20.002Z"}` + "\n"
}

func TestWSChatLLMTraceValidationAndNotFound(t *testing.T) {
	fixture := newChatExportWSTestFixture(t)
	conn := dialTestWS(t, fixture.server)
	defer conn.Close()

	for _, tc := range []struct {
		name    string
		id      string
		payload map[string]any
		code    int
		typeKey string
	}{
		{name: "missing", id: "req_missing_llm_trace", payload: map[string]any{}, code: http.StatusBadRequest, typeKey: "invalid_request"},
		{name: "invalid", id: "req_invalid_llm_trace", payload: map[string]any{"file": "../trace.json"}, code: http.StatusBadRequest, typeKey: "invalid_request"},
		{name: "not found", id: "req_not_found_llm_trace", payload: map[string]any{"file": "chat_1/.llm-records/missing_001.json"}, code: http.StatusNotFound, typeKey: "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := conn.WriteJSON(ws.RequestFrame{
				Frame:   ws.FrameRequest,
				Type:    "/api/chat/llm-trace",
				ID:      tc.id,
				Payload: ws.MarshalPayload(tc.payload),
			}); err != nil {
				t.Fatalf("write request: %v", err)
			}
			var frame ws.ErrorFrame
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("read error: %v", err)
			}
			if frame.Frame != ws.FrameError || frame.ID != tc.id || frame.Type != tc.typeKey || frame.Code != tc.code {
				t.Fatalf("unexpected error frame: %#v", frame)
			}
		})
	}
}

func TestLoadJSONLContentRejectsInvalidChatID(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.LoadJSONLContent("../chat"); err == nil {
		t.Fatalf("expected invalid chatId error")
	}
}

func dialTestWS(t *testing.T, server *Server) *gws.Conn {
	t.Helper()
	testServer := httptest.NewServer(server)
	t.Cleanup(testServer.Close)
	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	readConnectedPush(t, conn)
	return conn
}

func newChatExportWSTestFixture(t *testing.T) testFixture {
	t.Helper()
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.Logging.LLMInteraction.RecordDir = cfg.Paths.ChatsDir
		},
	})
}

func seedLLMTraceFile(t *testing.T, fixture testFixture, fileParam string, content string) {
	t.Helper()
	relativeFile, _, err := validateLLMTraceFileParam(fileParam)
	if err != nil {
		t.Fatalf("validate llm trace file: %v", err)
	}
	target := filepath.Join(fixture.cfg.Logging.LLMInteraction.RecordDir, filepath.FromSlash(relativeFile))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir llm trace dir: %v", err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write llm trace file: %v", err)
	}
}

func TestRenderChatMarkdownSkipsAutomationQuery(t *testing.T) {
	markdown := renderChatMarkdown("Automation", "agent-a", []stream.EventData{
		{
			Type:      "request.query",
			Timestamp: testEpochMillis + 100,
			Payload: map[string]any{
				"message": "Secret automation prompt",
				"role":    "automation",
			},
		},
		{
			Type:      "content.snapshot",
			Timestamp: testEpochMillis + 200,
			Payload: map[string]any{
				"text": "Automation result",
			},
		},
	})

	if strings.Contains(markdown, "Secret automation prompt") || strings.Contains(markdown, "## User") {
		t.Fatalf("expected automation query to be omitted, got:\n%s", markdown)
	}
	if !strings.Contains(markdown, "Automation result") {
		t.Fatalf("expected assistant content to remain, got:\n%s", markdown)
	}
}
