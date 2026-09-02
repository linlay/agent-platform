package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResourceHeadReturnsAuthoritativeDocumentMetadata(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID = "chat-resource-head"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "resource"); err != nil {
		t.Fatal(err)
	}
	relativePath := "artifacts/run-head/page.html"
	targetPath := filepath.Join(fixture.chats.ChatDir(chatID), filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("<!doctype html><title>Head</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	resourceURL := "/api/resource?file=" + url.QueryEscape(chatID+"/"+relativePath)
	recorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, resourceURL, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("HEAD status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("HEAD returned %d body bytes", recorder.Body.Len())
	}
	if got := recorder.Header().Get("X-ZenMind-Document-Kind"); got != "document-html" {
		t.Fatalf("document kind=%q", got)
	}
	if got := recorder.Header().Get("X-ZenMind-Resource-Revision"); got == "" {
		t.Fatal("missing resource revision")
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("content type=%q", got)
	}

	methodDenied := httptest.NewRecorder()
	fixture.server.ServeHTTP(methodDenied, httptest.NewRequest(http.MethodPost, resourceURL, nil))
	if methodDenied.Code != http.StatusMethodNotAllowed || methodDenied.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status=%d allow=%q", methodDenied.Code, methodDenied.Header().Get("Allow"))
	}
}
