package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"agent-platform/internal/conversationexport"
	"agent-platform/internal/stream"
)

const conversationExportTestTemplate = `<!doctype html><html><head><meta name="conversation-export-profile" content="conversation-snapshot-json-v1"><meta name="conversation-export-asset-set" content="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"><meta http-equiv="Content-Security-Policy" content="font-src __CONVERSATION_EXPORT_ASSET_ORIGIN__; style-src-elem __CONVERSATION_EXPORT_ASSET_ORIGIN__; script-src __CONVERSATION_EXPORT_ASSET_ORIGIN__"><link rel="stylesheet" href="__CONVERSATION_EXPORT_ASSET_ORIGIN__/assets/conversation-export/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/runtime.css" integrity="sha384-test" crossorigin="anonymous"></head><body><script id="conversation-snapshot" type="application/json">__CONVERSATION_EXPORT_SNAPSHOT_JSON_V1__</script><script src="__CONVERSATION_EXPORT_ASSET_ORIGIN__/assets/conversation-export/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/runtime.js" integrity="sha384-test" crossorigin="anonymous"></script></body></html>`

func TestHandleChatExportHTMLReturnsRenderedDocument(t *testing.T) {
	fixture := newTestFixture(t)
	seedCompletedConversationExport(t, fixture, "chat-html-export")
	fixture.server.conversationHTML = mustConversationHTMLRenderer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/chat/export?chatId=chat-html-export&format=html", nil)
	req.Header.Set(conversationExportAssetOriginHeader, "http://127.0.0.1:11961")
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("Content-Disposition") != `attachment; filename="rollback plan.html"` {
		t.Fatalf("unexpected headers: %#v", rec.Header())
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("content-length=%q body=%d", rec.Header().Get("Content-Length"), rec.Body.Len())
	}
	body := rec.Body.String()
	if strings.Contains(body, conversationexport.TemplateMarker) || strings.Contains(body, conversationexport.AssetOriginMarker) || !strings.Contains(body, `"version":1`) || !strings.Contains(body, "rollback completed") || !strings.Contains(body, "http://127.0.0.1:11961/assets/conversation-export/") {
		t.Fatalf("unexpected rendered HTML: %s", body)
	}
}

func TestHandleChatExportHTMLRequiresSafeAssetOrigin(t *testing.T) {
	fixture := newTestFixture(t)
	seedCompletedConversationExport(t, fixture, "chat-html-origin")
	fixture.server.conversationHTML = mustConversationHTMLRenderer(t)
	for _, origin := range []string{"", "http://public.example.test", "https://example.test/path"} {
		req := httptest.NewRequest(http.MethodGet, "/api/chat/export?chatId=chat-html-origin&format=html", nil)
		req.Header.Set(conversationExportAssetOriginHeader, origin)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("origin=%q status=%d body=%s", origin, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleChatExportHTMLUnavailableAndValidation(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		path string
		code int
	}{
		{path: "/api/chat/export?chatId=chat-1&format=html", code: http.StatusServiceUnavailable},
		{path: "/api/chat/export?chatId=../chat", code: http.StatusBadRequest},
		{path: "/api/chat/export?chatId=missing-chat", code: http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
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

func mustConversationHTMLRenderer(t *testing.T) *conversationexport.HTMLRenderer {
	t.Helper()
	renderer, err := conversationexport.NewHTMLRenderer([]byte(conversationExportTestTemplate))
	if err != nil {
		t.Fatalf("new HTML renderer: %v", err)
	}
	return renderer
}
