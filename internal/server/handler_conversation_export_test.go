package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"agent-platform/internal/conversationexport"
	"agent-platform/internal/stream"
)

func TestHandleChatExportSnapshotReturnsJSONDocument(t *testing.T) {
	fixture := newTestFixture(t)
	seedCompletedConversationExport(t, fixture, "chat-snapshot-export")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/chat/export?chatId=chat-snapshot-export&format=snapshot", nil)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/json; charset=utf-8" || rec.Header().Get("Content-Disposition") != `attachment; filename="rollback plan.snapshot.json"` {
		t.Fatalf("unexpected headers: %#v", rec.Header())
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("content-length=%q body=%d", rec.Header().Get("Content-Length"), rec.Body.Len())
	}
	var snapshot conversationexport.SnapshotV1
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.Version != conversationexport.SnapshotVersion || snapshot.Title != "rollback plan" || len(snapshot.Turns) != 1 || snapshot.Turns[0].Items[len(snapshot.Turns[0].Items)-1].Text != "rollback completed" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestHandleChatExportSnapshotValidation(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		path string
		code int
	}{
		{path: "/api/chat/export?chatId=chat-1&format=html", code: http.StatusBadRequest},
		{path: "/api/chat/export?chatId=../chat&format=snapshot", code: http.StatusBadRequest},
		{path: "/api/chat/export?chatId=missing-chat&format=snapshot", code: http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != tc.code {
			t.Fatalf("path=%s status=%d want=%d body=%s", tc.path, rec.Code, tc.code, rec.Body.String())
		}
	}
}

func TestConversationShareRoutesAreNotRegistered(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/chat/share"},
		{method: http.MethodGet, path: "/api/chat/shares?chatId=chat-1"},
		{method: http.MethodDelete, path: "/api/chat/share/share_abc"},
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status=%d want=%d body=%s", tc.method, tc.path, rec.Code, http.StatusNotFound, rec.Body.String())
		}
	}
}

func seedCompletedConversationExport(t *testing.T, fixture testFixture, chatID string) {
	t.Helper()
	seedSearchableChat(t, fixture.chats, chatID)
	for _, event := range []stream.EventData{
		{Type: "content.snapshot", Timestamp: testEpochMillis + 1_999, Payload: map[string]any{"runId": "run-" + chatID, "text": "rollback completed"}},
		{Type: "run.complete", Timestamp: testEpochMillis + 2_000, Payload: map[string]any{"runId": "run-" + chatID}},
	} {
		if err := fixture.chats.AppendEvent(chatID, event); err != nil {
			t.Fatalf("append %s event: %v", event.Type, err)
		}
	}
}
