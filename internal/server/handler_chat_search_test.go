package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
)

func TestHandleSessionSearchReturnsResults(t *testing.T) {
	chats, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	if _, _, err := chats.EnsureChat("chat-1", "agent-a", "", "Need rollback notes"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := chats.AppendQueryLine("chat-1", chat.QueryLine{
		ChatID: "chat-1", RunID: "run-1", UpdatedAt: 100, Type: "query",
		Query: map[string]any{"message": "Need rollback notes", "role": "user"},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}

	server := &Server{deps: Dependencies{Chats: chats}}
	body := bytes.NewBufferString(`{"chatId":"chat-1","query":"rollback","limit":5}`)
	req := httptest.NewRequest(http.MethodPost, "/api/session-search", body)
	rec := httptest.NewRecorder()

	server.handleSessionSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.SessionSearchResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Count != 1 || len(resp.Data.Results) != 1 {
		t.Fatalf("unexpected response: %#v", resp.Data)
	}
	if resp.Data.Results[0].Kind != "query" {
		t.Fatalf("unexpected result kind: %#v", resp.Data.Results[0])
	}
}
