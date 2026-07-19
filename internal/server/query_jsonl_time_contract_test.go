package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/ws"
)

func TestQueryRejectsInvalidPersistedTimeOverHTTPAndWS(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: ws.NewHub()})
	const chatID = "chat-query-jsonl-time-contract"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "first message"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	// Bypass the writer contract on purpose. Query preparation must reject the
	// invalid persisted record instead of proceeding with the run.
	if err := os.WriteFile(filepath.Join(fixture.cfg.Paths.ChatsDir, chatID+".jsonl"), []byte(`{"_type":"query","chatId":"`+chatID+`","runId":"run-query-jsonl-time-contract","updatedAt":0,"query":{"role":"user","message":"old message"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write invalid persisted line: %v", err)
	}

	body := []byte(`{"chatId":"` + chatID + `","agentKey":"mock-agent","message":"next message"}`)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(body)))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "time_contract_violation") {
		t.Fatalf("expected HTTP 422 time_contract_violation, status=%d body=%s", rec.Code, rec.Body.String())
	}

	conn := dialTestWS(t, fixture.server)
	defer conn.Close()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/query",
		ID:      "query-jsonl-time-contract",
		Payload: body,
	}); err != nil {
		t.Fatalf("write WS query: %v", err)
	}
	var frame ws.ErrorFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read WS error: %v", err)
	}
	if frame.Frame != ws.FrameError || frame.ID != "query-jsonl-time-contract" || frame.Code != http.StatusUnprocessableEntity || frame.Type != "time_contract_violation" {
		t.Fatalf("unexpected WS time violation frame: %#v", frame)
	}
}

func TestQueryRejectsPersistedMessageMissingTsOverHTTPAndWS(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: ws.NewHub()})
	const chatID = "chat-query-missing-message-ts"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "first message"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	line := `{"_type":"query","chatId":"` + chatID + `","runId":"run-query-missing-message-ts","updatedAt":1700000000001,"query":{"role":"user","message":"old message"},"messages":[{"role":"user","content":"old message"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(fixture.cfg.Paths.ChatsDir, chatID+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatalf("write invalid persisted line: %v", err)
	}

	body := []byte(`{"chatId":"` + chatID + `","agentKey":"mock-agent","message":"next message"}`)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(body)))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "time_contract_violation") {
		t.Fatalf("expected HTTP 422 time_contract_violation, status=%d body=%s", rec.Code, rec.Body.String())
	}

	conn := dialTestWS(t, fixture.server)
	defer conn.Close()
	if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/query", ID: "query-missing-message-ts", Payload: body}); err != nil {
		t.Fatalf("write WS query: %v", err)
	}
	var frame ws.ErrorFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read WS error: %v", err)
	}
	if frame.Frame != ws.FrameError || frame.ID != "query-missing-message-ts" || frame.Code != http.StatusUnprocessableEntity || frame.Type != "time_contract_violation" {
		t.Fatalf("unexpected WS time violation frame: %#v", frame)
	}
}

func TestRegisteredRunPersistsAuthoritativeStartAndActiveDetailOmitsCompletion(t *testing.T) {
	fixture := newTestFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"chatId":"chat-active-run-lifecycle","agentKey":"mock-agent","runId":"run-active-lifecycle","message":"hello"}`))
	prepared, err := prepareQueryForTest(fixture.server, req)
	if err != nil {
		t.Fatalf("prepare query: %v", err)
	}
	registered, statusErr := fixture.server.registerQueryRun(context.Background(), prepared)
	if statusErr != nil {
		t.Fatalf("register query run: %#v", statusErr)
	}
	defer fixture.server.finishRegisteredQueryRun(prepared, registered)

	detail, err := fixture.server.loadChatDetail(context.Background(), prepared.req.ChatID, false)
	if err != nil {
		t.Fatalf("load active detail: %v", err)
	}
	if detail.ActiveRun == nil || detail.ActiveRun.StartedAt != registered.StartedAtMillis {
		t.Fatalf("activeRun=%#v registered=%#v", detail.ActiveRun, registered)
	}
	if len(detail.Runs) != 0 {
		t.Fatalf("unfinished row must not appear in public runs: %#v", detail.Runs)
	}
	runs, err := fixture.chats.ListRuns(prepared.req.ChatID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("unfinished persisted row must be excluded: runs=%#v err=%v", runs, err)
	}
	if err := fixture.chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          prepared.req.ChatID,
		RunID:           prepared.req.RunID,
		AgentKey:        prepared.req.AgentKey,
		InitialMessage:  prepared.req.Message,
		AssistantText:   "done",
		StartedAtMillis: registered.StartedAtMillis,
		UpdatedAtMillis: registered.StartedAtMillis + 1,
	}); err != nil {
		t.Fatalf("complete persisted run: %v", err)
	}
	runs, err = fixture.chats.ListRuns(prepared.req.ChatID)
	if err != nil || len(runs) != 1 || runs[0].StartedAt != registered.StartedAtMillis {
		t.Fatalf("completed run must retain registered start: runs=%#v err=%v", runs, err)
	}
}
