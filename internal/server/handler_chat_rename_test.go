package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
)

func TestHandleChatRenameRenamesChat(t *testing.T) {
	store, server := newChatRenameTestServer(t)
	if _, _, err := store.EnsureChat("chat-rename", "agent-a", "", "old name"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}

	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"chatName":"New Name"}`)
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chat/rename?chatId=chat-rename", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.RenameChatResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Data.Updated || resp.Data.ChatID != "chat-rename" || resp.Data.ChatName != "New Name" {
		t.Fatalf("unexpected rename response: %#v", resp.Data)
	}
	summary, err := store.Summary("chat-rename")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary == nil || summary.ChatName != "New Name" {
		t.Fatalf("expected renamed summary, got %#v", summary)
	}
}

func TestRenamedOldActionRoutesAreRemoved(t *testing.T) {
	_, server := newChatRenameTestServer(t)
	for _, path := range []string{
		"/api/agent-create",
		"/api/chat-delete",
		"/api/automation-toggle",
		"/api/memory/records",
	} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{}`)))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: expected 404, got %d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func newChatRenameTestServer(t *testing.T) (*chat.FileStore, *Server) {
	t.Helper()
	store, err := chat.NewFileStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	server, err := New(Dependencies{Config: config.Config{}, Chats: store})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return store, server
}
