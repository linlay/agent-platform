package server

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"

	_ "modernc.org/sqlite"
)

func TestArchiveTimeContractViolationsReturn422OverHTTPAndWS(t *testing.T) {
	server, active, archives, chatsRoot := newStrictArchiveContractServer(t)

	// Invalid active data must stop archive before the archiver moves anything.
	writeChat := "chat-archive-write-time"
	seedStrictArchiveContractChat(t, active, writeChat)
	corruptArchiveContractDB(t, filepath.Join(chatsRoot, "chats.db"), "UPDATE CHATS SET LAST_RUN_AT_=? WHERE CHAT_ID_=?", int64(1_700_000_000), writeChat)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chat/archive", bytes.NewBufferString(`{"chatIds":["`+writeChat+`"]}`)))
	assertArchiveTimeViolationHTTP(t, rec)
	if summary, err := active.Summary(writeChat); err != nil || summary == nil {
		t.Fatalf("invalid archive must leave active chat intact: summary=%#v err=%v", summary, err)
	}

	// Corrupt an already archived row to exercise all public archive read paths.
	readChat := "chat-archive-read-time"
	seedStrictArchiveContractChat(t, active, readChat)
	if err := chat.NewArchiver(active, archives).ArchiveChat(readChat); err != nil {
		t.Fatalf("seed strict archive: %v", err)
	}
	corruptArchiveContractDB(t, filepath.Join(chatsRoot, "archive", "archive.db"), "UPDATE ARCHIVED_CHATS SET LAST_RUN_AT_=? WHERE CHAT_ID_=?", int64(1_700_000_000), readChat)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/archives", nil),
		httptest.NewRequest(http.MethodGet, "/api/archive?chatId="+readChat, nil),
		httptest.NewRequest(http.MethodPost, "/api/archives/search", bytes.NewBufferString(`{"query":"strict"}`)),
		httptest.NewRequest(http.MethodPost, "/api/archive/restore", bytes.NewBufferString(`{"chatIds":["`+readChat+`"]}`)),
	} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, request)
		assertArchiveTimeViolationHTTP(t, rec)
	}
	if summary, err := active.Summary(readChat); err != nil || summary != nil {
		t.Fatalf("invalid restore must not recreate active chat: summary=%#v err=%v", summary, err)
	}

	conn := dialTestWS(t, server)
	defer conn.Close()
	if err := conn.WriteJSON(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    "/api/archives",
		ID:      "archive-time-contract",
		Payload: ws.MarshalPayload(api.ArchivesRequest{}),
	}); err != nil {
		t.Fatalf("write WS request: %v", err)
	}
	var frame ws.ErrorFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read WS error: %v", err)
	}
	if frame.Frame != ws.FrameError || frame.ID != "archive-time-contract" || frame.Code != http.StatusUnprocessableEntity || frame.Type != "time_contract_violation" {
		t.Fatalf("unexpected WS time violation frame: %#v", frame)
	}
}

func newStrictArchiveContractServer(t *testing.T) (*Server, *chat.FileStore, *chat.ArchiveStore, string) {
	t.Helper()
	chatsRoot := filepath.Join(t.TempDir(), "chats")
	active, err := chat.NewFileStore(chatsRoot)
	if err != nil {
		t.Fatalf("new active store: %v", err)
	}
	archives, err := chat.NewArchiveStore(chatsRoot)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	server, err := New(Dependencies{
		Config:        config.Config{},
		Chats:         active,
		Archives:      archives,
		Archiver:      chat.NewArchiver(active, archives),
		Notifications: ws.NewHub(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return server, active, archives, chatsRoot
}

func seedStrictArchiveContractChat(t *testing.T, store *chat.FileStore, chatID string) {
	t.Helper()
	const (
		started   = int64(1_700_000_000_000)
		completed = int64(1_700_000_000_250)
	)
	if _, _, err := store.EnsureChat(chatID, "agent-a", "", "strict archive"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startServerFixtureRun(t, store, chatID, "run-"+chatID, started)
	if err := store.AppendQueryLine(chatID, chat.QueryLine{
		ChatID:    chatID,
		RunID:     "run-" + chatID,
		UpdatedAt: started,
		Query:     map[string]any{"role": "user", "message": "strict archive"},
		Type:      "query",
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.OnRunCompleted(chat.RunCompletion{
		ChatID:          chatID,
		RunID:           "run-" + chatID,
		AgentKey:        "agent-a",
		InitialMessage:  "strict archive",
		AssistantText:   "archive answer",
		FinishReason:    "complete",
		StartedAtMillis: started,
		UpdatedAtMillis: completed,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
}

func corruptArchiveContractDB(t *testing.T, path string, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("corrupt persisted time: %v", err)
	}
}

func assertArchiveTimeViolationHTTP(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "time_contract_violation") {
		t.Fatalf("expected 422 time_contract_violation, status=%d body=%s", rec.Code, rec.Body.String())
	}
}
